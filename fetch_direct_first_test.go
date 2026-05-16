package main

import (
	"testing"

	"github.com/anatolykoptev/go_job/internal/engine"
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
