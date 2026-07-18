package main

import "testing"

// initProxyPool(false) => nil: FETCH_DIRECT_FIRST=direct/off means fetch direct,
// no proxy pool at all.
func TestInitProxyPool_Disabled_ReturnsNil(t *testing.T) {
	if pool := initProxyPool(false); pool != nil {
		t.Fatalf("initProxyPool(false) = non-nil, want nil (direct fetch)")
	}
}

// Enabled + no Webshare key => Tor static pool (the new fallback: previously this
// path left ProxyPool nil and dropped to unproxied direct fetches).
func TestInitProxyPool_EnabledNoWebshare_TorFallback(t *testing.T) {
	t.Setenv("WEBSHARE_API_KEY", "")
	t.Setenv("TOR_PROXY", "socks5://127.0.0.1:9150")

	if pool := initProxyPool(true); pool == nil {
		t.Fatal("initProxyPool(true) = nil, want static Tor pool when no Webshare key")
	}
}

// Enabled + no Webshare key + empty TOR_PROXY => still a Tor pool (default addr).
func TestInitProxyPool_EnabledDefaultTor(t *testing.T) {
	t.Setenv("WEBSHARE_API_KEY", "")
	t.Setenv("TOR_PROXY", "")

	if pool := initProxyPool(true); pool == nil {
		t.Fatal("initProxyPool(true) = nil, want static Tor pool with default TOR_PROXY")
	}
}
