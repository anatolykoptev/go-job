package jobs

import (
	"os"
	"strings"
	"testing"
)

// TestATSDiscoveryRoutesThroughDiscoverJobURLs is a source-grep regression guard
// for the discovery-loss class: any connector that discovers company slugs / job
// URLs must route through discoverJobURLs (go-search primary + SearchDirect
// fallback), never call search functions directly. This prevents silently empty
// results when a search source is unavailable.
//
// This test greps the ATS / YC / Indeed / Craigslist connector sources and fails
// if any of them calls engine.SearchSearXNG or engine.SearchDirect directly for
// discovery instead of going through discoverJobURLs.
func TestATSDiscoveryRoutesThroughDiscoverJobURLs(t *testing.T) {
	files := []string{"ats.go", "ycjobs.go", "indeed.go", "craigslist.go"}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		src := stripDiscoverJobURLs(string(data))

		// discoverJobURLs is the single sanctioned wrapper; its body is stripped
		// above. Any REMAINING occurrence of a direct search call in these
		// connector files is a re-introduced bare dependency that bypasses the
		// go-search → SearchDirect fallback chain.
		if strings.Contains(src, "engine.SearchSearXNG(") {
			t.Errorf("%s calls engine.SearchSearXNG directly (outside discoverJobURLs) — "+
				"discovery must go through discoverJobURLs. SearXNG has been removed.", f)
		}
		if strings.Contains(src, "engine.SearchDirect(") {
			t.Errorf("%s calls engine.SearchDirect directly (outside discoverJobURLs) — "+
				"discovery must go through discoverJobURLs (go-search primary + DIRECT fallback).", f)
		}

		// Positive assertion: a connector file that discovers URLs must reference
		// the sanctioned helper.
		if strings.Contains(src, "SiteSearch") && !strings.Contains(string(data), "discoverJobURLs(") {
			t.Errorf("%s does site-scoped discovery but never calls discoverJobURLs", f)
		}
	}

	// Verify the wrapper exists and calls SearchDirect (the always-on fallback).
	atsData, err := os.ReadFile("ats.go")
	if err != nil {
		t.Fatalf("read ats.go: %v", err)
	}
	ats := string(atsData)
	if !strings.Contains(ats, "func discoverJobURLs(") {
		t.Fatal("discoverJobURLs helper missing from ats.go")
	}
	idx := strings.Index(ats, "func discoverJobURLs(")
	if idx < 0 {
		t.Fatal("discoverJobURLs helper missing from ats.go")
	}
	body := ats[idx:]
	if end := strings.Index(body, "\nfunc "); end > 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "engine.SearchDirect(") {
		t.Error("discoverJobURLs must call engine.SearchDirect (the always-on fallback path)")
	}
}

// stripDiscoverJobURLs removes the discoverJobURLs and searchWeb function bodies
// from a source string so the bare-search check does not flag the sanctioned
// calls inside those wrappers. Returns src unchanged when the helpers are absent.
func stripDiscoverJobURLs(src string) string {
	for _, fn := range []string{"func discoverJobURLs(", "func searchWeb("} {
		for {
			idx := strings.Index(src, fn)
			if idx < 0 {
				break
			}
			rest := src[idx:]
			if end := strings.Index(rest, "\nfunc "); end > 0 {
				src = src[:idx] + rest[end:]
			} else {
				src = src[:idx]
			}
		}
	}
	return src
}
