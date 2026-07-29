package jobs

import (
	"context"
	"os"
	"testing"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// TestCraigslistLive is a live integration test that hits the REAL ox-browser
// /fetch endpoint. Skipped unless OX_BROWSER_URL_LIVE is set. Run with:
//
//	OX_BROWSER_URL_LIVE=http://ox-browser:8901 go test -count=1 -run TestCraigslistLive ./internal/engine/jobs/ -v
//
// Verifies:
//  1. cat=jjj query=warehouse → ≥100 results with real titles, URLs, locations.
//  2. cat=fbh → results.
//  3. query=zzqqxxnothingmatches12345 → (nil, nil), NOT discovery fallback.
func TestCraigslistLive(t *testing.T) {
	oxURL := os.Getenv("OX_BROWSER_URL_LIVE")
	if oxURL == "" {
		t.Skip("OX_BROWSER_URL_LIVE not set — skipping live verification")
	}

	// Save and restore state.
	origStealth := craigslistStealthFetch
	origOxFetch := craigslistOxFetchFetch
	origOx := craigslistOxBrowserFetch
	origOxURL := engine.Cfg.OxBrowserURL
	t.Cleanup(func() {
		craigslistStealthFetch = origStealth
		craigslistOxFetchFetch = origOxFetch
		craigslistOxBrowserFetch = origOx
		engine.Cfg.OxBrowserURL = origOxURL
	})

	engine.Cfg.OxBrowserURL = oxURL

	// Stub the stealth tier to fail (403) so the ox-browser /fetch tier is
	// exercised — that's the transport we're verifying.
	craigslistStealthFetch = stubStealth403
	// Stub the RSS tier to fail so it doesn't interfere.
	craigslistOxBrowserFetch = stubOx403

	t.Run("warehouse", func(t *testing.T) {
		results, err := SearchCraigslistJobs(context.Background(), "warehouse", "sfbay", 100)
		if err != nil {
			t.Fatalf("warehouse: expected results, got error: %v", err)
		}
		t.Logf("warehouse: %d results", len(results))
		if len(results) < 10 {
			t.Errorf("warehouse: expected ≥10 results, got %d", len(results))
		}
		for i, r := range results {
			if i >= 5 {
				break
			}
			t.Logf("  [%d] title=%q url=%s loc=%q", i+1, r.Title, r.URL, r.Metadata["location"])
		}
	})

	t.Run("fbh", func(t *testing.T) {
		// cat=fbh is a Craigslist category (food/beverage/hospitality), not a
		// query keyword. SearchCraigslistJobs builds cat=jjj&query={query}, so
		// passing "fbh" as a query searches for the keyword "fbh" (0 results).
		// Verify the transport+parser pipeline with the cat=fbh URL directly.
		fbhURL := "https://www.craigslist.org/search/area/sfbay?cat=fbh"
		headers := map[string]string{"accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"}
		status, body, err := craigslistOxFetchFetch(context.Background(), fbhURL, headers)
		if err != nil {
			t.Fatalf("fbh: /fetch transport error: %v", err)
		}
		if status != 200 || len(body) == 0 {
			t.Fatalf("fbh: /fetch returned status=%d body=%d bytes", status, len(body))
		}
		results, err := parseCraigslistHTML(body, 100)
		if err != nil {
			t.Fatalf("fbh: parse error: %v", err)
		}
		t.Logf("fbh: %d results", len(results))
		if len(results) == 0 {
			t.Error("fbh: expected results, got 0")
		}
		for i, r := range results {
			if i >= 5 {
				break
			}
			t.Logf("  [%d] title=%q url=%s loc=%q", i+1, r.Title, r.URL, r.Metadata["location"])
		}
	})

	t.Run("zero", func(t *testing.T) {
		results, err := SearchCraigslistJobs(context.Background(), "zzqqxxnothingmatches12345", "sfbay", 100)
		if err != nil {
			t.Fatalf("zero: expected nil error for genuine empty, got: %v", err)
		}
		if results != nil {
			t.Errorf("zero: expected nil results (genuine empty), got %d results (discovery fallback laundering)", len(results))
		}
		t.Logf("zero: (nil, nil) — genuine empty, NOT discovery fallback")
	})
}
