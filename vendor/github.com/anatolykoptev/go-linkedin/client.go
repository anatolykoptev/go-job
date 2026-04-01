package linkedin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	stealth "github.com/anatolykoptev/go-stealth"
)

const (
	baseURL          = "https://www.linkedin.com"
	defaultMaxReq    = 80
	defaultJitterMin = 3 * time.Second
	defaultJitterMax = 10 * time.Second
	versionTTL       = 24 * time.Hour
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
}

// Client is a LinkedIn Voyager API client with stealth transport and rate limiting.
type Client struct {
	bc       *stealth.BrowserClient
	cookies  map[string]string
	cfg      ClientConfig
	limiter  *RateLimiter
	verCache versionCache
}

// New creates a new LinkedIn Voyager client with the given configuration.
func New(cfg ClientConfig) (*Client, error) {
	cfg.defaults()
	opts := []stealth.ClientOption{
		stealth.WithHeaderOrder(linkedinHeaderOrder),
	}
	if cfg.Proxy != "" {
		opts = append(opts, stealth.WithProxy(cfg.Proxy))
	}
	bc, err := stealth.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("stealth client: %w", err)
	}
	return &Client{
		bc:      bc,
		cookies: cfg.Cookies,
		cfg:     cfg,
		limiter: NewRateLimiter(cfg.MaxReqPerDay, 24*time.Hour),
	}, nil
}

func (c *Client) do(ctx context.Context, endpoint string) ([]byte, error) {
	if !c.limiter.Allow() {
		return nil, fmt.Errorf("linkedin rate limit exhausted (%d remaining)", c.limiter.Remaining())
	}
	time.Sleep(jitterDuration(c.cfg.JitterMin, c.cfg.JitterMax))
	version := c.clientVersion(ctx)
	csrf := extractCSRFToken(c.cookies["JSESSIONID"])
	headers := buildHeaders(csrf, version, c.cfg.UserAgent, c.cfg.SecChUA)
	headers["Cookie"] = buildCookieString(c.cookies)
	body, _, statusCode, err := c.bc.DoWithHeaderOrderCtx(ctx, "GET", baseURL+endpoint, headers, nil, linkedinHeaderOrder)
	if err != nil {
		return nil, fmt.Errorf("voyager request %s: %w", endpoint, err)
	}
	if statusCode == 401 || statusCode == 403 {
		return nil, fmt.Errorf("voyager auth failed: status %d (cookies may be expired)", statusCode)
	}
	if statusCode != 200 {
		return nil, fmt.Errorf("voyager %s: status %d", endpoint, statusCode)
	}
	return body, nil
}

func (c *Client) clientVersion(ctx context.Context) string {
	if v, ok := c.verCache.get(); ok {
		return v
	}
	headers := map[string]string{
		"Cookie": buildCookieString(c.cookies),
	}
	v, err := scrapeClientVersion(ctx, c.bc, headers)
	if err != nil {
		slog.Warn("failed to scrape LinkedIn clientVersion, using fallback", slog.Any("error", err))
		return "1.13.43122.3"
	}
	c.verCache.set(v, versionTTL)
	return v
}

func safeUnmarshal(data json.RawMessage, v any) error {
	if data == nil {
		return fmt.Errorf("nil data")
	}
	return json.Unmarshal(data, v)
}

// Remaining returns the number of API requests remaining in the current window.
func (c *Client) Remaining() int {
	return c.limiter.Remaining()
}
