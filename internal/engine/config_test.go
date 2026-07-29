package engine

import (
	"testing"
)

// TestResolveBrowserClient_FallbackCoversNilProxy (H5): when the proxy-backed
// BrowserClient is nil (direct-first mode: FETCH_DIRECT_FIRST=direct →
// ProxyPool nil → BrowserClient() nil), the config assembly MUST fall back to
// the no-proxy direct Chrome-TLS client. Without this fallback, connectors
// that guard on `Cfg.BrowserClient != nil` (e.g. Craigslist) silently skip the
// stealth tier in direct mode and report empty results.
//
// This test exercises the fallback helper extracted from Init() — Init() is
// too heavy to spin up in a unit test (goroutines, metrics, LLM ping), and
// every craigslist unit test overrides the transport vars wholesale, so the
// real fallback body never ran under test before this.
//
// MUTATION-CHECK: revert resolveBrowserClient to `return proxy` (drop the
// direct fallback) → the nil-proxy case returns nil instead of direct → red.
// Both runs pasted in the PR report.
func TestResolveBrowserClient_FallbackCoversNilProxy(t *testing.T) {
	proxy := &BrowserClient{}
	direct := &BrowserClient{}

	// Proxy present → proxy wins (no fallback needed).
	if got := resolveBrowserClient(proxy, direct); got != proxy {
		t.Errorf("proxy present: expected proxy client, got %p (want %p)", got, proxy)
	}

	// Proxy nil, direct present → fallback to direct. THIS is the case H5
	// guards: reverting the fallback returns nil here.
	if got := resolveBrowserClient(nil, direct); got != direct {
		t.Errorf("proxy nil, direct present: expected direct client (fallback), got %p (want %p) — fallback was dropped", got, direct)
	}

	// Both nil → nil (neither tier built).
	if got := resolveBrowserClient(nil, nil); got != nil {
		t.Errorf("both nil: expected nil, got %p", got)
	}
}
