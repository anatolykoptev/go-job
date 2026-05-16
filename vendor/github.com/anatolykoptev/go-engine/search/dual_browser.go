package search

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// dualBrowser tries primary BrowserDoer first; on transport error or quota-exhausted
// HTTP status (402, 407, 5xx) falls back to the secondary doer.
//
// Use case: primary is a proxy-backed stealth client (e.g. Webshare); fallback is
// a plain http.Client without proxy. When proxy bandwidth is exhausted (402) or
// the proxy gateway is down (407/5xx), search engines that don't require TLS
// fingerprinting (Reddit JSON, Yep API, Yandex API, DDG html) still succeed.
type dualBrowser struct {
	primary  BrowserDoer
	fallback BrowserDoer
}

// newDualBrowser returns primary if fallback is nil; otherwise wraps both.
func newDualBrowser(primary, fallback BrowserDoer) BrowserDoer {
	if fallback == nil {
		return primary
	}
	return &dualBrowser{primary: primary, fallback: fallback}
}

func (d *dualBrowser) Do(method, url string, headers map[string]string, body io.Reader) ([]byte, map[string]string, int, error) {
	bodyBytes, err := snapshotBody(body)
	if err != nil {
		return nil, nil, 0, err
	}

	data, hdr, status, err := d.primary.Do(method, url, headers, readerFor(bodyBytes))
	if err == nil && !shouldFallback(status) {
		return data, hdr, status, nil
	}

	slog.Warn("dual_browser: primary failed, trying fallback",
		slog.String("url", url),
		slog.Int("status", status),
		slog.Any("error", err))

	return d.fallback.Do(method, url, headers, readerFor(bodyBytes))
}

// shouldFallback returns true for proxy-quota or proxy-gateway statuses that
// indicate the primary's transport (not the target) failed.
func shouldFallback(status int) bool {
	switch status {
	case http.StatusPaymentRequired, // 402: Webshare bandwidth exhausted
		http.StatusProxyAuthRequired, // 407: proxy auth failed
		http.StatusBadGateway,        // 502: proxy can't reach target
		http.StatusServiceUnavailable, // 503: proxy overloaded
		http.StatusGatewayTimeout:     // 504: proxy upstream timeout
		return true
	}
	return false
}

// snapshotBody buffers the body once so it can be replayed for the fallback call.
// nil body is preserved as nil to avoid forcing Content-Length: 0 on GETs.
func snapshotBody(body io.Reader) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	return io.ReadAll(body)
}

func readerFor(b []byte) io.Reader {
	if b == nil {
		return nil
	}
	return bytes.NewReader(b)
}

// HTTPDoer wraps a standard *http.Client as a BrowserDoer.
// No proxy, no TLS fingerprinting — suitable as fallback for endpoints that
// don't require stealth (JSON APIs, simple HTML).
type HTTPDoer struct {
	Client *http.Client
}

// NewHTTPDoer creates a direct HTTP doer without proxy.
func NewHTTPDoer() *HTTPDoer {
	return &HTTPDoer{Client: &http.Client{Timeout: 15 * time.Second}}
}

func (d *HTTPDoer) Do(method, url string, headers map[string]string, body io.Reader) ([]byte, map[string]string, int, error) {
	// BrowserDoer.Do interface predates ctx propagation; the wrapping layer
	// (fetch.RetryDo) carries cancellation. Client.Timeout bounds latency.
	req, err := http.NewRequest(method, url, body) //nolint:noctx // interface boundary, see comment above
	if err != nil {
		return nil, nil, 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := d.Client.Do(req) //nolint:gosec // URL is caller-supplied by design
	if err != nil {
		return nil, nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, 0, err
	}
	rh := make(map[string]string, len(resp.Header))
	for k := range resp.Header {
		rh[k] = resp.Header.Get(k)
	}
	return data, rh, resp.StatusCode, nil
}
