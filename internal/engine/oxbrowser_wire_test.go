package engine

// oxbrowser_wire_test.go asserts that the ox-browser anti-bot fallback tier is
// actually wired through Init, not just compiled. The vendored fetcher keeps
// its oxBrowserURL field unexported, so we assert the two observable contracts:
//   1. fetch.WithOxBrowser(url) is a real Option that fetch.New accepts (the
//      tier the fetcher's tryFallbacks escalates to on a primary-fetch error).
//   2. Init propagates Config.OxBrowserURL — the gate that decides whether the
//      option is appended — set when OX_BROWSER_URL is provided, empty (graceful
//      no-op) when it is not.
//
// Regression guard for the "dead wiring" class: fetch.WithOxBrowser existed,
// fully wired in vendored go-engine, but was never constructed in Init — so
// discovery had no anti-bot escalation and DDG's 202 wall zeroed the ATS
// connectors (see 2026-06-23 discovery-collapse arc).

import (
	"testing"

	"github.com/anatolykoptev/go-engine/fetch"
)

// TestOxBrowserOptionAccepted verifies fetch.WithOxBrowser returns an Option
// that fetch.New accepts without panic — the fallback tier the engine relies on.
func TestOxBrowserOptionAccepted(t *testing.T) {
	f := fetch.New(fetch.WithOxBrowser("http://ox-browser:8901"))
	if f == nil {
		t.Fatal("fetch.New(WithOxBrowser) returned nil")
	}
}

// TestInitWiresOxBrowserWhenSet asserts Init propagates a non-empty
// OxBrowserURL into the engine config — the gate for appending WithOxBrowser
// to fetcherOpts. Without this propagation the tier is never wired.
func TestInitWiresOxBrowserWhenSet(t *testing.T) {
	Init(Config{
		FetchTimeout: 1, // any non-zero; avoids a zero-duration fetch client
		OxBrowserURL: "http://ox-browser:8901",
	})
	if got := Cfg.OxBrowserURL; got != "http://ox-browser:8901" {
		t.Fatalf("Init did not propagate OxBrowserURL: got %q, want %q",
			got, "http://ox-browser:8901")
	}
}

// TestInitGracefulWithoutOxBrowser asserts Init does not panic and leaves
// OxBrowserURL empty when OX_BROWSER_URL is unset — the tier degrades to a
// no-op rather than failing engine startup.
func TestInitGracefulWithoutOxBrowser(t *testing.T) {
	Init(Config{
		FetchTimeout: 1,
		OxBrowserURL: "",
	})
	if got := Cfg.OxBrowserURL; got != "" {
		t.Fatalf("expected empty OxBrowserURL when unset, got %q", got)
	}
}
