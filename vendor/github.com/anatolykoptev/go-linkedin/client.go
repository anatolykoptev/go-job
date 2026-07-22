package linkedin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	stealth "github.com/anatolykoptev/go-stealth"
)

// ErrSessionExpired is returned by do() when the JSESSIONID cookie is missing
// or empty, meaning no CSRF token can be derived for the request. Consumers
// can branch on this via errors.Is instead of inspecting the message string.
var ErrSessionExpired = errors.New("linkedin: session expired (JSESSIONID missing)")

const (
	baseURL          = "https://www.linkedin.com"
	defaultMaxReq    = 50
	defaultJitterMin = 15 * time.Second
	defaultJitterMax = 45 * time.Second
	versionTTL       = 24 * time.Hour

	// SessionNameLinkedIn is the single home for the go-wowa named session
	// used by the CDP in-page-fetch transport. go-job/go-social consume this
	// constant; a fitness-fn forbids the string literal elsewhere.
	SessionNameLinkedIn = "linkedin-voyager"
)

var linkedinHeaderOrder = []string{
	"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform",
	"user-agent", "accept", "accept-language",
	"csrf-token", "x-li-track", "x-restli-protocol-version",
	"sec-fetch-site", "sec-fetch-mode", "sec-fetch-dest",
	"cookie",
}

// ClientConfig holds configuration for the LinkedIn Voyager client.
type ClientConfig struct {
	Cookies      map[string]string
	Proxy        string
	UserAgent    string
	SecChUA      string
	MaxReqPerDay int
	JitterMin    time.Duration
	JitterMax    time.Duration
	// OnChallenge is called when Login() encounters an App Challenge (mobile approve).
	OnChallenge func(challengeID string)
	// OnEmailPin is called when Login() encounters an email PIN challenge.
	// Must return the 6-digit PIN code. If nil, returns ChallengeError.
	OnEmailPin func(email string) (string, error)
	// ClientVersionFallback is used when the clientVersion scrape fails.
	// If unset, a scrape failure causes do() to return an error instead of
	// silently using a hardcoded version (a config-drift signal LinkedIn can
	// use to detect automation). Setting this is a conscious operator decision.
	ClientVersionFallback string
	// WowaURL is the base URL of the go-wowa service (e.g. http://go-wowa:8906).
	// When set, Voyager GETs are routed through go-wowa's evaluate seam as
	// in-page fetches from a /feed/-pinned tab — the fix for LinkedIn's
	// Cloudflare 302-to-self loop. When empty, the stealth do() path is used.
	WowaURL string
	// Session is the go-wowa named session handle for the CDP transport.
	// Defaults to SessionNameLinkedIn when empty.
	Session string
	// InternalSecret is sent as X-Internal-Secret on go-wowa requests
	// (soft-auth from the foundation arc). Send always; harmless when unused.
	InternalSecret string
}

func (c *ClientConfig) defaults() {
	if c.MaxReqPerDay <= 0 {
		c.MaxReqPerDay = defaultMaxReq
	}
	if c.JitterMin <= 0 {
		c.JitterMin = defaultJitterMin
	}
	if c.JitterMax <= 0 {
		c.JitterMax = defaultJitterMax
	}
	if c.Session == "" {
		c.Session = SessionNameLinkedIn
	}
}

// Client is a LinkedIn Voyager API client with stealth transport and rate limiting.
type Client struct {
	mu            sync.RWMutex
	bc            *stealth.BrowserClient
	wowa          *wowaTransport // CDP in-page-fetch transport via go-wowa; nil = stealth fallback
	cookies       map[string]string
	cfg           ClientConfig
	limiter       *RateLimiter
	verCache      versionCache
	testBaseURL   string // overrides baseURL in tests
	testScrapeURL string // overrides LinkedIn homepage URL for version scraping in tests

	// getJobDetailFn is an injectable seam for SearchJobs tests. When nil,
	// SearchJobs uses the real GetJobDetail method.
	getJobDetailFn func(context.Context, string) (*JobDetail, error)
}

// New creates a new LinkedIn Voyager client with the given configuration.
func New(cfg ClientConfig) (*Client, error) {
	cfg.defaults()
	opts := []stealth.ClientOption{
		stealth.WithHeaderOrder(linkedinHeaderOrder),
		stealth.WithTimeout(60),
	}
	if cfg.Proxy != "" {
		opts = append(opts, stealth.WithProxy(cfg.Proxy))
	}
	bc, err := stealth.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("stealth client: %w", err)
	}
	c := &Client{
		bc:      bc,
		cookies: cfg.Cookies,
		cfg:     cfg,
		limiter: NewRateLimiter(cfg.MaxReqPerDay, 24*time.Hour),
	}
	if cfg.WowaURL != "" {
		c.wowa = newWowaTransport(cfg.WowaURL, cfg.InternalSecret)
	}
	return c, nil
}

func (c *Client) do(ctx context.Context, endpoint string) ([]byte, error) {
	if !c.limiter.Allow() {
		return nil, fmt.Errorf("linkedin rate limit exhausted (%d remaining)", c.limiter.Remaining())
	}
	jitter := jitterDuration(c.cfg.JitterMin, c.cfg.JitterMax)
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("linkedin: cancelled during jitter: %w", ctx.Err())
	case <-time.After(jitter):
	}
	// CDP in-page-fetch transport via go-wowa (fixes CF 302-to-self loop).
	// When configured, route Voyager GETs through the shared cloak-browser's
	// evaluate seam — same-origin fetch from a /feed/-pinned tab. The stealth
	// path below stays as the kill-switch/fallback (byte-for-byte unchanged).
	if c.wowa != nil {
		return c.doCDP(ctx, endpoint)
	}
	version, err := c.clientVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("linkedin: clientVersion unavailable: %w", err)
	}
	c.mu.RLock()
	csrf := extractCSRFToken(c.cookies["JSESSIONID"])
	hasJSessionID := c.cookies["JSESSIONID"] != ""
	headers, hErr := buildHeaders(csrf, version, c.cfg.UserAgent, c.cfg.SecChUA)
	if hErr != nil {
		return nil, fmt.Errorf("linkedin: build headers: %w", hErr)
	}
	headers["Cookie"] = buildCookieString(c.cookies)
	c.mu.RUnlock()
	if csrf == "" {
		slog.Warn("do: refusing request — JSESSIONID cookie missing, cannot derive CSRF token",
			slog.String("csrf_token", csrf),
			slog.Bool("has_jsessionid", hasJSessionID),
		)
		return nil, fmt.Errorf("linkedin: JSESSIONID cookie missing — cannot derive CSRF token: %w", ErrSessionExpired)
	}
	target := baseURL
	if c.testBaseURL != "" {
		target = c.testBaseURL
	}
	body, _, statusCode, err := c.bc.DoWithHeaderOrderCtx(ctx, "GET", target+endpoint, headers, nil, linkedinHeaderOrder)
	if err != nil {
		return nil, fmt.Errorf("voyager request %s: %w", endpoint, err)
	}
	if statusCode == 401 || statusCode == 403 {
		return nil, &VoyagerStatusError{Endpoint: endpoint, Status: statusCode}
	}
	if statusCode == 200 && len(body) > 0 && body[0] == '<' {
		return nil, &VoyagerHTMLResponseError{Endpoint: endpoint}
	}
	if statusCode != 200 {
		return nil, &VoyagerStatusError{Endpoint: endpoint, Status: statusCode}
	}
	return body, nil
}

func (c *Client) clientVersion(ctx context.Context) (string, error) {
	if v, ok := c.verCache.get(); ok {
		return v, nil
	}
	c.mu.RLock()
	headers := map[string]string{
		"Cookie": buildCookieString(c.cookies),
	}
	c.mu.RUnlock()
	scrapeURL := c.testScrapeURL
	v, err := scrapeClientVersion(ctx, c.bc, headers, scrapeURL)
	if err != nil {
		if c.cfg.ClientVersionFallback != "" {
			slog.Warn("clientVersion scrape failed, using configured fallback",
				slog.Any("error", err),
				slog.String("fallback", c.cfg.ClientVersionFallback))
			c.verCache.set(c.cfg.ClientVersionFallback, versionTTL)
			return c.cfg.ClientVersionFallback, nil
		}
		slog.Error("clientVersion scrape failed, no fallback configured", slog.Any("error", err))
		return "", fmt.Errorf("clientVersion scrape failed, no fallback configured: %w", err)
	}
	c.verCache.set(v, versionTTL)
	return v, nil
}

func safeUnmarshal(data json.RawMessage, v any) error {
	if data == nil {
		return fmt.Errorf("nil data")
	}
	return json.Unmarshal(data, v)
}

// Cookies returns a copy of the current session cookies.
func (c *Client) Cookies() map[string]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make(map[string]string, len(c.cookies))
	for k, v := range c.cookies {
		result[k] = v
	}
	return result
}

// Remaining returns the number of API requests remaining in the current window.
func (c *Client) Remaining() int {
	return c.limiter.Remaining()
}
