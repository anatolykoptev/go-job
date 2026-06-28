package main

import (
	"testing"

	"github.com/anatolykoptev/go_job/internal/engine"
	twitterpkg "github.com/anatolykoptev/go-twitter"
)

// TestResolveFetchMode validates resolveFetchMode against all documented and
// edge-case inputs. Tests drive production logic directly — not a replicated
// expression — so a typo in main.go is caught immediately.
func TestResolveFetchMode(t *testing.T) {
	cases := []struct {
		input        string
		wantDirect   bool
		wantInitPool bool
	}{
		{input: "auto", wantDirect: true, wantInitPool: true},
		{input: "direct", wantDirect: true, wantInitPool: false},
		{input: "proxy", wantDirect: false, wantInitPool: true},
		{input: "off", wantDirect: false, wantInitPool: false},
		// Case normalization.
		{input: "AUTO", wantDirect: true, wantInitPool: true},
		{input: " auto ", wantDirect: true, wantInitPool: true},
		// Unknown values fall back to proxy-first + pool init.
		{input: "auot", wantDirect: false, wantInitPool: true},
		{input: "", wantDirect: false, wantInitPool: true},
	}

	for _, tc := range cases {
		t.Run("FETCH_DIRECT_FIRST="+tc.input, func(t *testing.T) {
			gotDirect, gotInitPool := resolveFetchMode(tc.input)
			if gotDirect != tc.wantDirect {
				t.Errorf("directFirst: got %v, want %v", gotDirect, tc.wantDirect)
			}
			if gotInitPool != tc.wantInitPool {
				t.Errorf("initPool: got %v, want %v", gotInitPool, tc.wantInitPool)
			}
		})
	}
}

// TestEngineConfigFetchDirectFirstField ensures engine.Config.FetchDirectFirst
// field is visible and assignable (compile-time coverage for the new field).
func TestEngineConfigFetchDirectFirstField(t *testing.T) {
	c := engine.Config{
		FetchDirectFirst: true,
	}
	if !c.FetchDirectFirst {
		t.Fatal("FetchDirectFirst field should be true")
	}
	c.FetchDirectFirst = false
	if c.FetchDirectFirst {
		t.Fatal("FetchDirectFirst field should be false after reset")
	}
}

// TestTwitterClientConfigSilentFallbackFields ensures the ClientConfig fields
// used for silent-fallback mode when go-social is configured are accessible and
// assignable (compile-time coverage for the fix in initEngine).
func TestTwitterClientConfigSilentFallbackFields(t *testing.T) {
	// Verify that the two fields the fix uses exist on twitter.ClientConfig
	// and carry the correct zero values (OpenAccountCount=0 suppresses guest
	// bootstrap; DisableGuestFallback=true blocks the guest-token fallback path).
	cfg := twitterpkg.ClientConfig{
		OpenAccountCount:     0,
		DisableGuestFallback: true,
	}
	if cfg.OpenAccountCount != 0 {
		t.Fatalf("OpenAccountCount: got %d, want 0", cfg.OpenAccountCount)
	}
	if !cfg.DisableGuestFallback {
		t.Fatal("DisableGuestFallback: got false, want true")
	}
}
