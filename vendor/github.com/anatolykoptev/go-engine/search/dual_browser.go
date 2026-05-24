package search

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strconv"
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
	logger   *slog.Logger
}

// newDualBrowser returns primary if fallback is nil (or typed nil); otherwise wraps both.
//
// The typed-nil check via isNilInterface guards against the Go pitfall where an
// interface holding a typed-nil pointer is NOT == nil. Real-world example: the
// 2026-05-16 go-search panic where fb=fetcherDirect.BrowserClient() returned a
// typed-nil *stealth.BrowserClient; assigned to BrowserDoer it passed `== nil`,
// then (*stealth.BrowserClient).Do(nil,...) panicked. See isNilInterface for details.
func newDualBrowser(primary, fallback BrowserDoer) BrowserDoer {
	return newDualBrowserWithLogger(primary, fallback, slog.Default())
}

// newDualBrowserWithLogger is newDualBrowser with an injected logger (used in tests).
func newDualBrowserWithLogger(primary, fallback BrowserDoer, logger *slog.Logger) BrowserDoer {
	if isNilInterface(fallback) {
		return primary
	}
	return &dualBrowser{primary: primary, fallback: fallback, logger: logger}
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

	reason := fallbackReason(status, err)
	d.logger.Warn("dual_browser: primary failed, trying fallback",
		slog.String("url", url),
		slog.Int("status", status),
		slog.String("reason", reason),
		slog.Any("error", err))

	return d.fallback.Do(method, url, headers, readerFor(bodyBytes))
}

// fallbackReason returns a bounded label for the cause of fallback. Labels:
//   - "net_err" — err != nil (connection/transport failure)
//   - "402"     — Webshare bandwidth exhausted
//   - "407"     — proxy auth failed
//   - "5xx"     — 502/503/504 (proxy gateway errors)
//
// These labels are logged as slog reason attrs and are candidates for a future
// Prometheus counter (deferred to a followup PR — search/ lacks metrics plumbing).
func fallbackReason(status int, err error) string {
	if err != nil {
		return "net_err"
	}
	switch status {
	case http.StatusPaymentRequired:
		return "402"
	case http.StatusProxyAuthRequired:
		return "407"
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return "5xx"
	}
	return strconv.Itoa(status)
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

// isNilInterface returns true for both a nil interface AND an interface holding
// a typed-nil pointer. The latter is a common Go gotcha — assigning a typed-nil
// pointer (e.g. var c *T = nil) to an interface variable produces a non-nil
// interface (the interface holds a non-nil type descriptor with a nil value pointer).
//
// See: https://go.dev/doc/faq#nil_error and the 2026-05-16 go-search prod panic
// for a real-world example: fb = fetcherDirect.BrowserClient() returned a
// (*stealth.BrowserClient)(nil); when assigned to search.BrowserDoer the `== nil`
// check passed, dualBrowser was constructed, and Do() panicked.
func isNilInterface(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func, reflect.Interface:
		return rv.IsNil()
	}
	return false
}
