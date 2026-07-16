// Package fetch provides HTTP body retrieval with retry, proxy rotation,
// and Chrome TLS fingerprint impersonation via go-stealth.
//
// The [Fetcher] is the main entry point. Configure with functional options:
//
//	f := fetch.New(fetch.WithProxyPool(pool), fetch.WithTimeout(30*time.Second))
//	body, err := f.FetchBody(ctx, "https://example.com")
package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	kithttputil "github.com/anatolykoptev/go-kit/httputil"
	stealth "github.com/anatolykoptev/go-stealth"
	"github.com/anatolykoptev/go-stealth/proxypool"
)

// Default configuration values.
const (
	defaultTimeout             = 30 * time.Second
	defaultMaxIdleConns        = 20
	defaultMaxIdleConnsPerHost = 5
	defaultIdleConnTimeout     = 30 * time.Second
	defaultTLSHandshakeTimeout = 15 * time.Second
	defaultMaxRedirects        = 10
	browserClientTimeoutSec    = 15
)

// RetryConfig controls retry behavior (re-exported from go-stealth).
type RetryConfig = stealth.RetryConfig

// DefaultRetryConfig is suitable for most HTTP calls.
var DefaultRetryConfig = stealth.DefaultRetryConfig

// FetchRetryConfig is tuned for web page fetching (slower initial, more patience).
var FetchRetryConfig = RetryConfig{
	MaxRetries:  3,
	InitialWait: 1 * time.Second,
	MaxWait:     10 * time.Second,
	Multiplier:  2.0,
}

// stealthDoer is a minimal interface satisfied by *stealth.BrowserClient.
// Extracted at consumer point for testability (interface small = easy to mock).
// Both directClient and proxyClient use this interface.
type stealthDoer interface {
	DoCtx(ctx context.Context, method, urlStr string, headers map[string]string, body io.Reader) ([]byte, map[string]string, int, error)
}

// Fetcher retrieves HTTP response bodies with optional proxy routing.
type Fetcher struct {
	httpClient        *http.Client
	browserClient     *stealth.BrowserClient
	proxyClient       stealthDoer // proxy-tier doer; set to browserClient in New(), injectable in tests
	directClient      stealthDoer // Chrome TLS, no proxy — used for direct-first tier; *stealth.BrowserClient in prod, mock in tests
	retryConfig       RetryConfig
	retryTracker      *stealth.RetryTracker
	proxyPool         proxypool.ProxyPool    // deferred: used to build browserClient in New()
	cookieProvider    stealth.CookieProvider // deferred: passed to stealth.NewClient in New()
	byparrURL         string                 // Byparr fallback URL (empty = disabled)
	oxBrowserURL      string                 // ox-browser /fetch-smart fallback (empty = disabled)
	goBrowserURL      string                 // go-browser /render fallback (empty = disabled)
	directFirst       bool                   // when true, try direct before proxy
	blockCache        *DirectBlockCache      // tracks hosts that blocked direct requests
	proxyFirstDomains *ProxyFirstDomains     // domains that always skip direct tier
	metrics           fetchMetrics           // tier observability; noopMetrics unless WithMetrics is called
}

// Option configures a Fetcher.
type Option func(*Fetcher)

// WithTimeout sets the HTTP client timeout.
func WithTimeout(d time.Duration) Option {
	return func(f *Fetcher) { f.httpClient.Timeout = d }
}

// WithRetryConfig sets the retry configuration.
func WithRetryConfig(rc RetryConfig) Option {
	return func(f *Fetcher) { f.retryConfig = rc }
}

// WithProxyPool enables proxy rotation via a ProxyPool.
// The BrowserClient is created in New() after all options are applied,
// so WithCookieSolver can be combined regardless of option order.
func WithProxyPool(pool proxypool.ProxyPool) Option {
	return func(f *Fetcher) {
		if pool != nil {
			f.proxyPool = pool
		}
	}
}

// WithCookieSolver enables Cloudflare challenge solving via go-stealth's CookieProvider.
// Requires WithProxyPool (no effect without a BrowserClient).
func WithCookieSolver(provider stealth.CookieProvider) Option {
	return func(f *Fetcher) {
		f.cookieProvider = provider
	}
}

// WithDirectFirst enables the direct-first tiered fallback strategy.
//
// When true, FetchBody attempts a direct Chrome-TLS request (no proxy) first,
// escalating to the proxy tier only when anti-bot signals are detected:
// HTTP 403/429/503, Cloudflare/PerimeterX/DataDome/Imperva challenge bodies,
// soft-block heuristic (200 OK + body <512B + text/html), or connection errors.
//
// When false (default), legacy proxy-first behaviour is preserved — byte-identical
// to the previous implementation. Consumers must opt in explicitly.
func WithDirectFirst(enabled bool) Option {
	return func(f *Fetcher) { f.directFirst = enabled }
}

// WithProxyFirstHosts extends the built-in proxy-first domain list with extra entries.
// The entries are domain suffixes (e.g. "example.com" matches "sub.example.com").
// Has no effect when WithDirectFirst(false) (default).
func WithProxyFirstHosts(hosts []string) Option {
	return func(f *Fetcher) {
		if f.proxyFirstDomains == nil {
			f.proxyFirstDomains = NewProxyFirstDomains(hosts)
		} else {
			// Merge into a fresh slice to avoid mutating caller's backing array.
			merged := make([]string, 0, len(hosts)+len(defaultProxyFirstHosts))
			merged = append(merged, hosts...)
			merged = append(merged, defaultProxyFirstHosts...)
			f.proxyFirstDomains = NewProxyFirstDomains(merged)
		}
	}
}

// WithBlockTTL sets the direct-block cache TTL (default 10 minutes).
// Has no effect when WithDirectFirst(false) (default).
func WithBlockTTL(d time.Duration) Option {
	return func(f *Fetcher) {
		if f.blockCache == nil {
			f.blockCache = NewDirectBlockCache(d, defaultBlockCacheCap)
		} else {
			f.blockCache.ttl = d
		}
	}
}

// WithMetrics wires a fetchMetrics implementation into the Fetcher for tier
// observability. Use NewPromMetrics(prometheus.DefaultRegisterer) for production.
// Not called by default — metrics are no-ops until this option is applied.
func WithMetrics(m fetchMetrics) Option {
	return func(f *Fetcher) { f.metrics = m }
}

// New creates a Fetcher with the given options.
func New(opts ...Option) *Fetcher {
	f := &Fetcher{
		httpClient: &http.Client{
			Timeout: defaultTimeout,
			Transport: &http.Transport{
				MaxIdleConns:        defaultMaxIdleConns,
				MaxIdleConnsPerHost: defaultMaxIdleConnsPerHost,
				IdleConnTimeout:     defaultIdleConnTimeout,
				DisableCompression:  false,
				TLSHandshakeTimeout: defaultTLSHandshakeTimeout,
			},
			CheckRedirect: func(_ *http.Request, via []*http.Request) error {
				if len(via) >= defaultMaxRedirects {
					return fmt.Errorf("stopped after %d redirects", defaultMaxRedirects)
				}
				return nil
			},
		},
		retryConfig: FetchRetryConfig,
		metrics:     noopMetrics{},
	}
	for _, o := range opts {
		o(f)
	}

	// Build BrowserClient after all options are applied (order-independent).
	// Proxied tier: the dial target is always the proxy's vantage point, so
	// WithDialControl would only ever see the proxy IP (proxy-blind) — not
	// wired here. WithRequestURLGuard (tier-3) still upgrades the pre-request
	// check on the initial URL from go-stealth's stdlib floor to go-kit's
	// fuller policy (CGNAT / NAT64 / alt-encoded-IP rejection).
	if f.proxyPool != nil {
		stealthOpts := []stealth.ClientOption{
			stealth.WithTimeout(browserClientTimeoutSec),
			stealth.WithProxyPool(f.proxyPool),
			stealth.WithFollowRedirects(),
			stealth.WithRetryOnBlock(2),
			stealth.WithRequestURLGuard(kithttputil.CheckURL),
		}
		if f.cookieProvider != nil {
			stealthOpts = append(stealthOpts, stealth.WithCookieSolver(f.cookieProvider))
		}
		if bc, err := stealth.NewClient(stealthOpts...); err == nil {
			f.browserClient = bc
			f.proxyClient = bc
		}
	}

	// Build a no-proxy Chrome-TLS client for the direct tier (directFirst mode only).
	// Direct tier dials the target from this container itself, so it gets the
	// full three-tier go-kit SSRF policy: DialControl (connect-time,
	// rebind-proof) + RedirectGuard (per-hop pre-resolve) + RequestURLGuard
	// (initial URL). See go-stealth v1.18.0's stdlib default-deny floor,
	// which this wiring upgrades to go-kit's richer IsBlockedIP policy.
	// Skip if directClient was already injected (e.g. in tests).
	if f.directFirst && f.directClient == nil {
		redirectGuard, dialControl := kithttputil.SSRFGuards()
		if dc, err := stealth.NewClient(
			stealth.WithTimeout(browserClientTimeoutSec),
			stealth.WithFollowRedirects(),
			stealth.WithDialControl(dialControl),
			stealth.WithRedirectGuard(redirectGuard),
			stealth.WithRequestURLGuard(kithttputil.CheckURL),
		); err == nil {
			f.directClient = dc
		}
		if f.blockCache == nil {
			f.blockCache = NewDirectBlockCache(defaultBlockTTL, defaultBlockCacheCap)
		}
		if f.proxyFirstDomains == nil {
			f.proxyFirstDomains = NewProxyFirstDomains(nil)
		}
	}

	return f
}

// FetchBody retrieves the response body bytes from a URL.
// Routes through BrowserClient (residential proxy) when available,
// falls back to standard HTTP client otherwise.
// When a RetryTracker is configured, it checks ShouldRetry before each request
// and records the outcome (attempt or success) after.
func (f *Fetcher) FetchBody(ctx context.Context, url string) ([]byte, error) {
	return f.FetchBodyWithHeaders(ctx, url, nil)
}

// FetchBodyWithHeaders retrieves the response body bytes from a URL, merging
// extra HTTP headers over the built-in Chrome browser defaults.
//
// Keys in extra are normalised to lowercase before merging so that
//
//	extra["Accept"] = "application/json"
//
// correctly overrides the built-in "accept" header. Passing nil extra is
// equivalent to calling [FetchBody] — all tiering, fallback, block-cache,
// and metrics behaviour is preserved.
func (f *Fetcher) FetchBodyWithHeaders(ctx context.Context, url string, extra map[string]string) ([]byte, error) {
	permanent := f.retryTracker != nil && !f.retryTracker.ShouldRetry(url)
	if permanent && !f.hasFallback() {
		return nil, ErrPermanentlyFailed
	}

	body, err := f.fetchPrimaryWithHeaders(ctx, url, permanent, extra)
	body, err = f.tryFallbacks(ctx, url, body, err)

	if f.retryTracker != nil {
		if err != nil {
			f.retryTracker.RecordAttempt(url, err)
		} else {
			f.retryTracker.RecordSuccess(url)
		}
	}

	return body, err
}

// mergeHeaders returns a copy of ChromeHeaders with extra merged over it.
// Keys in extra are normalised to lowercase to match the ChromeHeaders convention
// (all-lowercase keys) so a single "accept" key survives regardless of input case.
// Passing nil extra returns a plain ChromeHeaders copy.
func mergeHeaders(extra map[string]string) map[string]string {
	h := ChromeHeaders()
	for k, v := range extra {
		h[strings.ToLower(k)] = v
	}
	return h
}

// hasFallback reports whether any fallback renderer is configured.
func (f *Fetcher) hasFallback() bool {
	return f.oxBrowserURL != "" || f.goBrowserURL != "" || f.byparrURL != ""
}

// fetchPrimaryWithHeaders attempts the primary fetch method with optional extra headers.
//
// When directFirst is enabled the order is:
//  1. Domain hint (proxyFirstDomains) → straight to proxy.
//  2. blockCache hit → straight to proxy (repeat-blocked host within TTL).
//  3. Direct attempt via directClient (Chrome TLS, no proxy).
//  4. classifyBlock → if sigHard|sigSoft|sigTLS → mark host in blockCache, escalate to proxy.
//  5. No proxy available → return direct result as-is.
//
// When directFirst is disabled (default) the legacy proxy-first behaviour is preserved.
// extra is forwarded to all HTTP call sites and merged over built-in Chrome defaults.
func (f *Fetcher) fetchPrimaryWithHeaders(ctx context.Context, url string, permanent bool, extra map[string]string) ([]byte, error) {
	if permanent {
		return nil, ErrPermanentlyFailed
	}

	// Legacy mode: proxy-first (or plain HTTP when no proxy configured).
	if !f.directFirst {
		return f.fetchViaProxyOrHTTPWithHeaders(ctx, url, extra)
	}

	return f.fetchDirectFirstWithHeaders(ctx, url, extra)
}

// fetchViaProxyOrHTTPWithHeaders routes through proxy if available, otherwise plain HTTP.
// extra is merged over built-in Chrome headers on each request.
func (f *Fetcher) fetchViaProxyOrHTTPWithHeaders(ctx context.Context, url string, extra map[string]string) ([]byte, error) {
	if f.proxyClient != nil {
		return f.fetchViaProxy(ctx, url, extra)
	}
	return f.fetchViaHTTP(ctx, url, extra)
}

// fetchDirectFirstWithHeaders implements the direct-first tiered fallback strategy.
// extra is merged over built-in Chrome headers on every outbound request.
// Steps:
//  1. Domain hint (proxyFirstDomains) → straight to proxy.
//  2. blockCache hit → straight to proxy (repeat-blocked host within TTL).
//  3. Direct attempt via directClient (Chrome TLS, no proxy).
//  4. classifyBlock → if blocked signal → mark host, escalate to proxy.
//  5. No proxy available → return direct result/error.
func (f *Fetcher) fetchDirectFirstWithHeaders(ctx context.Context, url string, extra map[string]string) ([]byte, error) {
	// 1. Domain hint → skip direct, go straight to proxy.
	if f.proxyFirstDomains != nil && f.proxyFirstDomains.MatchURL(url) {
		f.metrics.incEscalation("domain_hint")
		return f.fetchProxyAndRecordWithHeaders(ctx, url, extra)
	}

	// 2. blockCache hit → skip direct, go straight to proxy.
	var host string
	if u, err := neturl.Parse(url); err == nil {
		host = u.Host
	}
	if f.blockCache != nil && f.blockCache.IsBlocked(host) {
		f.metrics.incEscalation("cached")
		return f.fetchProxyAndRecordWithHeaders(ctx, url, extra)
	}

	// 3. Direct attempt via Chrome-TLS directClient (no proxy).
	body, hdrs, status, directErr := f.fetchDirectRaw(ctx, url, extra)

	// 4. Classify block signal.
	sig := classifyBlock(status, hdrs, body, directErr)
	if sig == sigNone {
		// Direct succeeded cleanly.
		if directErr != nil {
			f.metrics.incTier("direct", "err")
			return nil, directErr
		}
		f.metrics.incTier("direct", "ok")
		return body, nil
	}

	// Blocked — record signal, mark host in cache.
	f.metrics.incBlockSignal(sig.label())
	if f.blockCache != nil && host != "" {
		f.blockCache.Mark(host)
		f.metrics.setBlockCacheHosts(f.blockCache.Len())
	}

	// 5. Escalate to proxy if available.
	if f.proxyClient != nil {
		f.metrics.incEscalation(sig.label())
		return f.fetchProxyAndRecordWithHeaders(ctx, url, extra)
	}

	// No proxy budget — handling depends on signal strength:
	// - sigSoft: body is real content (suspect but not a hard error); return it
	//   and let the caller decide. Returning HttpStatusError{200} would be
	//   semantically confusing (200 OK as error).
	// - sigTLS: connection-level failure; return the original transport error.
	// - sigHard: hard block; return an HttpStatusError with the actual status.
	if sig == sigSoft {
		return body, nil
	}
	if directErr != nil {
		return nil, directErr
	}
	return nil, &HttpStatusError{StatusCode: status}
}

// fetchProxyAndRecordWithHeaders routes to proxy tier, records the tier counter,
// and forwards extra headers.
func (f *Fetcher) fetchProxyAndRecordWithHeaders(ctx context.Context, url string, extra map[string]string) ([]byte, error) {
	body, err := f.fetchViaProxyOrHTTPWithHeaders(ctx, url, extra)
	if err != nil {
		f.metrics.incTier("proxy", "err")
	} else {
		f.metrics.incTier("proxy", "ok")
	}
	return body, err
}

// tryFallbacks runs the fallback chain when the primary fetch fails.
// Order: ox-browser → go-browser /render → legacy Byparr.
// Each fallback gets the shorter of its own timeout or the parent context deadline.
func (f *Fetcher) tryFallbacks(ctx context.Context, url string, body []byte, err error) ([]byte, error) {
	if err != nil && f.oxBrowserURL != "" {
		obCtx, obCancel := childTimeout(ctx, oxBrowserTimeout+5*time.Second)
		defer obCancel()
		if fallback, obErr := f.fetchViaOxBrowser(obCtx, url); obErr == nil {
			return fallback, nil
		}
	}

	if err != nil && f.goBrowserURL != "" {
		if ctx.Err() != nil {
			return body, err
		}
		gbCtx, gbCancel := childTimeout(ctx, goBrowserTimeout)
		defer gbCancel()
		if fallback, gbErr := f.fetchViaGoBrowser(gbCtx, url); gbErr == nil {
			return fallback, nil
		}
	}

	if err != nil && f.byparrURL != "" {
		if ctx.Err() != nil {
			return body, err
		}
		fbCtx, fbCancel := childTimeout(ctx, byparrTimeout)
		defer fbCancel()
		if fallback, fbErr := f.fetchViaByparr(fbCtx, url); fbErr == nil {
			return fallback, nil
		}
	}

	return body, err
}

// childTimeout returns a context with the shorter of maxDur or the parent's remaining deadline.
func childTimeout(parent context.Context, maxDur time.Duration) (context.Context, context.CancelFunc) {
	if deadline, ok := parent.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining < maxDur {
			return context.WithTimeout(parent, remaining)
		}
	}
	return context.WithTimeout(parent, maxDur)
}

// HasProxy reports whether the fetcher has a proxy-backed BrowserClient.
func (f *Fetcher) HasProxy() bool {
	return f.proxyClient != nil
}

// BrowserClient returns the underlying stealth BrowserClient, or nil.
// Use this to share the browser client with search functions (DDG, Startpage).
func (f *Fetcher) BrowserClient() *stealth.BrowserClient {
	return f.browserClient
}

// DirectClient returns the no-proxy Chrome-TLS stealth client built when
// WithDirectFirst(true) was set. Returns nil otherwise. Consumers should
// nil-check before use.
//
// Implementation note: the internal directClient field is typed as stealthDoer
// (an unexported interface) to allow test injection of mocks. In production the
// concrete type is always *stealth.BrowserClient (built by New()). The type
// assertion returns nil for test doubles, which is safe — callers only use
// DirectClient() to obtain the real client for wiring into search.BrowserDoer.
func (f *Fetcher) DirectClient() *stealth.BrowserClient {
	if f.directClient == nil {
		return nil
	}
	bc, _ := f.directClient.(*stealth.BrowserClient)
	return bc
}

// fetchViaProxy routes through the proxy-tier stealthDoer (BrowserClient with Chrome TLS fingerprint).
// extra headers are merged over the built-in Chrome defaults; caller-supplied values take
// precedence (e.g. extra["Accept"]="application/json" overrides the legacy HTML accept).
func (f *Fetcher) fetchViaProxy(ctx context.Context, fetchURL string, extra map[string]string) ([]byte, error) {
	headers := ChromeHeaders()
	headers["accept"] = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
	for k, v := range extra {
		headers[strings.ToLower(k)] = v
	}

	return RetryDo(ctx, f.retryConfig, func() ([]byte, error) {
		data, _, status, err := f.proxyClient.DoCtx(ctx, http.MethodGet, fetchURL, headers, nil)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, &stealth.HttpStatusError{StatusCode: status}
		}
		return data, nil
	})
}

// fetchViaHTTP uses the standard HTTP client with retry.
// extra headers are merged over the built-in defaults; caller-supplied values
// take precedence (e.g. extra["Accept"]="application/json" overrides "text/html").
func (f *Fetcher) fetchViaHTTP(ctx context.Context, fetchURL string, extra map[string]string) ([]byte, error) {
	resp, err := RetryHTTP(ctx, f.retryConfig, func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", RandomUserAgent())
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		req.Header.Set("Accept-Encoding", "gzip, deflate")
		// Merge caller-supplied headers over the defaults above.
		// http.Header.Set canonicalises keys, so extra["Accept"] and extra["accept"]
		// both target the same canonical slot.
		for k, v := range extra {
			req.Header.Set(k, v)
		}
		return f.httpClient.Do(req) //nolint:gosec // URL is user-provided by design
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &stealth.HttpStatusError{StatusCode: resp.StatusCode}
	}

	return ReadResponseBody(resp)
}

// fetchDirectRaw issues a single direct request via directClient (Chrome TLS, no proxy)
// and returns the raw response components without retry.
// extra headers are merged over built-in Chrome defaults before sending; caller-supplied
// values take precedence (e.g. extra["Accept"]="application/json" overrides legacy HTML accept).
// Returns zero values and an error on connection failure.
func (f *Fetcher) fetchDirectRaw(ctx context.Context, fetchURL string, extra map[string]string) (body []byte, hdrs http.Header, status int, err error) {
	if f.directClient == nil {
		return nil, nil, 0, errNoDirectClient
	}

	headers := ChromeHeaders()
	headers["accept"] = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
	for k, v := range extra {
		headers[strings.ToLower(k)] = v
	}

	data, respHdrs, respStatus, doErr := f.directClient.DoCtx(ctx, http.MethodGet, fetchURL, headers, nil)
	if doErr != nil {
		return nil, nil, 0, doErr
	}

	// Convert stealth response headers (map[string]string) to http.Header.
	h := make(http.Header, len(respHdrs))
	for k, v := range respHdrs {
		h.Set(k, v)
	}

	return data, h, respStatus, nil
}

// errNoDirectClient is returned when fetchDirectRaw is called without a directClient.
var errNoDirectClient = errors.New("fetch: directClient not initialised")
