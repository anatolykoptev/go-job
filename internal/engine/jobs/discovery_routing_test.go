package jobs

import (
	"os"
	"strings"
	"testing"
)

// stripDiscoverJobURLs removes the discoverJobURLs function body from a source
// string so the bare-SearXNG check does not flag the sanctioned additive call
// inside the wrapper itself. Returns src unchanged when the helper is absent.
func stripDiscoverJobURLs(src string) string {
	idx := strings.Index(src, "func discoverJobURLs(")
	if idx < 0 {
		return src
	}
	rest := src[idx:]
	if end := strings.Index(rest, "\nfunc "); end > 0 {
		return src[:idx] + rest[end:]
	}
	return src[:idx]
}

// TestATSDiscoveryDoesNotDependOnBareSearXNG is a source-grep regression guard
// for the discovery-loss class: SEARXNG_URL is unset in prod, so any connector
// that discovers company slugs / job URLs via engine.SearchSearXNG ALONE returns
// silently empty (searxngInst==nil → nil,nil → no slugs → no jobs). The fix
// routes discovery through discoverJobURLs (go-engine DIRECT primary + SearXNG
// additive), mirroring the #53 company-research fix.
//
// This test greps the ATS / YC / Indeed connector sources and fails if any of
// them calls engine.SearchSearXNG directly for discovery instead of going
// through discoverJobURLs. It goes RED if a future edit reintroduces the bare
// dead-SearXNG dependency.
func TestATSDiscoveryDoesNotDependOnBareSearXNG(t *testing.T) {
	files := []string{"ats.go", "ycjobs.go", "indeed.go"}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		src := stripDiscoverJobURLs(string(data))

		// discoverJobURLs is the single sanctioned wrapper that calls
		// SearchSearXNG (additively); its body is stripped above. Any REMAINING
		// occurrence of SearchSearXNG in these connector files is a re-introduced
		// bare dependency that returns silently empty when SEARXNG_URL is unset.
		if strings.Contains(src, "engine.SearchSearXNG(") {
			t.Errorf("%s calls engine.SearchSearXNG directly (outside discoverJobURLs) — "+
				"discovery must go through discoverJobURLs (DIRECT primary + SearXNG "+
				"additive). Bare SearXNG returns silently empty when SEARXNG_URL is unset.", f)
		}

		// Positive assertion: a connector file that discovers URLs must reference
		// the sanctioned helper.
		if strings.Contains(src, "SiteSearch") && !strings.Contains(string(data), "discoverJobURLs(") {
			t.Errorf("%s does site-scoped discovery but never calls discoverJobURLs", f)
		}
	}

	// The wrapper itself is allowed to (and must) call SearchSearXNG additively,
	// alongside SearchDirect. Verify the wrapper still binds both sources.
	atsData, err := os.ReadFile("ats.go")
	if err != nil {
		t.Fatalf("read ats.go: %v", err)
	}
	ats := string(atsData)
	if !strings.Contains(ats, "func discoverJobURLs(") {
		t.Fatal("discoverJobURLs helper missing from ats.go")
	}
	// Extract the helper body and confirm it fans out to BOTH DIRECT and SearXNG.
	idx := strings.Index(ats, "func discoverJobURLs(")
	if idx < 0 {
		t.Fatal("discoverJobURLs helper missing from ats.go")
	}
	body := ats[idx:]
	if end := strings.Index(body, "\nfunc "); end > 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "engine.SearchDirect(") {
		t.Error("discoverJobURLs must call engine.SearchDirect (the always-on primary path)")
	}
	if !strings.Contains(body, "engine.SearchSearXNG(") {
		t.Error("discoverJobURLs must call engine.SearchSearXNG (additive when configured)")
	}
}
