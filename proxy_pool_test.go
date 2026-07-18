package main

import "testing"

// initProxyPool(false) => nil: FETCH_DIRECT_FIRST=direct/off means fetch direct,
// no proxy pool at all.
func TestInitProxyPool_Disabled_ReturnsNil(t *testing.T) {
	if pool := initProxyPool(false); pool != nil {
		t.Fatalf("initProxyPool(false) = non-nil, want nil (direct fetch)")
	}
}

// Enabled + no Webshare + Tor fallback NOT enabled (default) => nil (direct).
// This is the pre-change behaviour: a Webshare outage must not silently route
// through Tor unless the operator opts in.
func TestInitProxyPool_NoWebshare_TorGateOff_ReturnsNil(t *testing.T) {
	t.Setenv("WEBSHARE_API_KEY", "")
	t.Setenv("TOR_FALLBACK_ENABLED", "")

	if pool := initProxyPool(true); pool != nil {
		t.Fatalf("initProxyPool(true) = non-nil, want nil (direct) when TOR_FALLBACK_ENABLED unset")
	}
}

// Enabled + no Webshare + Tor fallback ON => static Tor pool at the configured addr.
func TestInitProxyPool_NoWebshare_TorGateOn_TorPool(t *testing.T) {
	t.Setenv("WEBSHARE_API_KEY", "")
	t.Setenv("TOR_FALLBACK_ENABLED", "true")
	t.Setenv("TOR_PROXY", "socks5://127.0.0.1:9150")

	pool := initProxyPool(true)
	if pool == nil {
		t.Fatal("initProxyPool(true) = nil, want static Tor pool when TOR_FALLBACK_ENABLED=true")
	}
	if got, want := pool.Len(), 1; got != want {
		t.Fatalf("pool.Len() = %d, want %d (single static Tor entry, not a Webshare pool)", got, want)
	}
	if got, want := pool.Next(), "socks5://127.0.0.1:9150"; got != want {
		t.Fatalf("pool.Next() = %q, want %q", got, want)
	}
}

// Tor fallback ON + empty TOR_PROXY => static pool at the default docker addr.
func TestInitProxyPool_TorGateOn_DefaultTor(t *testing.T) {
	t.Setenv("WEBSHARE_API_KEY", "")
	t.Setenv("TOR_FALLBACK_ENABLED", "true")
	t.Setenv("TOR_PROXY", "")

	pool := initProxyPool(true)
	if pool == nil {
		t.Fatal("initProxyPool(true) = nil, want static Tor pool with default TOR_PROXY")
	}
	if got, want := pool.Next(), "socks5://tor:9050"; got != want {
		t.Fatalf("pool.Next() = %q, want %q (default TOR_PROXY)", got, want)
	}
}
