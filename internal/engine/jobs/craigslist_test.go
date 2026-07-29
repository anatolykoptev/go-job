package jobs

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	stealth "github.com/anatolykoptev/go-stealth"
	"github.com/anatolykoptev/go_job/internal/engine"
)

// --- Test helpers ---

// saveFetchVars saves the current fetch function variables and restores them on cleanup.
func saveFetchVars(t *testing.T) {
	t.Helper()
	origStealth := craigslistStealthFetch
	origOx := craigslistOxBrowserFetch
	t.Cleanup(func() {
		craigslistStealthFetch = origStealth
		craigslistOxBrowserFetch = origOx
	})
}

// stubStealth403 returns a 403 blocked response (the Craigslist IP-block signature).
func stubStealth403(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
	return http.StatusForbidden, []byte("<!DOCTYPE html><html><head><title>blocked</title></head></html>"), nil
}

// stubStealthSuccess returns the given body with status 200.
func stubStealthSuccess(body []byte) func(context.Context, string, map[string]string) (int, []byte, error) {
	return func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		return http.StatusOK, body, nil
	}
}

// stubOxSuccess returns the given body with status 200.
func stubOxSuccess(body []byte) func(context.Context, string, map[string]string) (int, []byte, error) {
	return func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		return http.StatusOK, body, nil
	}
}

// stubOx403 returns a 403 — both tiers refused.
func stubOx403(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
	return http.StatusForbidden, []byte("blocked"), nil
}

// stubOxError returns a transport error.
func stubOxError(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
	return 0, nil, errors.New("ox-browser: connection refused")
}

// --- Tests ---

// TestParseCraigslistRSS_RealFixture validates the parser against a REAL captured
// Craigslist RSS feed (RDF/XML, RSS 1.0 format). The fixture is from
// anaisbetts/cl-apartment-finder — a real sfbay.craigslist.org RSS capture (23 items).
//
// The live jobs feed (jjj) is IP-blocked from the datacenter (403 on both stealth
// and ox-browser tiers), so this apartment-category fixture stands in: the XML
// shape is identical across categories. This catches the parser-format bug where
// the old code expected RSS 2.0 (<rss><channel><item>) but Craigslist serves
// RSS 1.0 (<rdf:RDF><item> with dc:date namespace).
func TestParseCraigslistRSS_RealFixture(t *testing.T) {
	body, err := os.ReadFile("testdata/craigslist_rss_real.xml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	results, err := parseCraigslistRSS(body, 100)
	if err != nil {
		t.Fatalf("parseCraigslistRSS failed on real fixture: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("parseCraigslistRSS returned 0 results from a real 23-item fixture — parser does not handle the RDF/XML format Craigslist serves")
	}

	// Verify the first result has all expected fields populated.
	first := results[0]
	if first.Title == "" {
		t.Error("first result has empty title")
	}
	if first.URL == "" {
		t.Error("first result has empty URL")
	}
	if !strings.Contains(first.URL, "craigslist.org") {
		t.Errorf("first result URL does not contain craigslist.org: %s", first.URL)
	}
	// Content should contain "Source: Craigslist" and a posted date.
	if !strings.Contains(first.Content, "Craigslist") {
		t.Errorf("first result content missing source tag: %s", first.Content)
	}
	if !strings.Contains(first.Content, "Posted:") {
		t.Errorf("first result content missing posted date: %s", first.Content)
	}

	t.Logf("parsed %d results from real fixture; first: title=%q url=%q", len(results), first.Title, first.URL)
}

// TestSearchCraigslistJobs_Stealth403OxBrowserSuccess verifies the two-tier
// escalation: stealth returns 403, ox-browser returns the feed → results returned.
// This is the observable escalation: if the fallback didn't fire, we'd get a
// blocked error instead of results.
func TestSearchCraigslistJobs_Stealth403OxBrowserSuccess(t *testing.T) {
	saveFetchVars(t)

	fixture, err := os.ReadFile("testdata/craigslist_rss_real.xml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	craigslistStealthFetch = stubStealth403
	craigslistOxBrowserFetch = stubOxSuccess(fixture)

	results, err := SearchCraigslistJobs(context.Background(), "warehouse", "sfbay", 100)
	if err != nil {
		t.Fatalf("expected results, got error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results from ox-browser fallback, got 0 — escalation did not fire")
	}
}

// TestSearchCraigslistJobs_BothTiers403_ReturnsBlockedError is the EXACT
// regression that shipped: both tiers refused (403) and the connector returned
// (nil, nil) — reporting success with zero rows. Must return an error, NOT nil.
func TestSearchCraigslistJobs_BothTiers403_ReturnsBlockedError(t *testing.T) {
	saveFetchVars(t)

	craigslistStealthFetch = stubStealth403
	craigslistOxBrowserFetch = stubOx403

	results, err := SearchCraigslistJobs(context.Background(), "warehouse", "sfbay", 100)
	if err == nil {
		t.Fatal("expected blocked error when both tiers return 403, got nil — this is the shipped defect (nil, nil → outcome=empty, error=0)")
	}
	if results != nil {
		t.Errorf("expected nil results on blocked, got %d results", len(results))
	}
}

// TestSearchCraigslistJobs_BothTiersFail_ReturnsError verifies that transport
// errors (not just 403) also produce an error, not silent success.
func TestSearchCraigslistJobs_BothTiersFail_ReturnsError(t *testing.T) {
	saveFetchVars(t)

	craigslistStealthFetch = func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		return 0, nil, errors.New("stealth: dial timeout")
	}
	craigslistOxBrowserFetch = stubOxError

	results, err := SearchCraigslistJobs(context.Background(), "warehouse", "sfbay", 100)
	if err == nil {
		t.Fatal("expected error when both tiers fail with transport errors, got nil")
	}
	if results != nil {
		t.Errorf("expected nil results on failure, got %d results", len(results))
	}
}

// TestSearchCraigslistJobs_ValidEmptyRSS_ReturnsNil verifies that a valid RSS
// feed with zero items returns (nil, nil) — genuine empty stays genuine empty.
func TestSearchCraigslistJobs_ValidEmptyRSS_ReturnsNil(t *testing.T) {
	saveFetchVars(t)

	emptyRDF := `<?xml version="1.0" encoding="UTF-8"?>
<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns="http://purl.org/rss/1.0/" xmlns:dc="http://purl.org/dc/elements/1.1/">
<channel rdf:about="https://sfbay.craigslist.org/search/jjj?format=rss">
<title>craigslist SF bay area | all jobs search </title>
<link>https://sfbay.craigslist.org/search/jjj</link>
<description></description>
<items><rdf:Seq></rdf:Seq></items>
</channel>
</rdf:RDF>`

	craigslistStealthFetch = stubStealthSuccess([]byte(emptyRDF))
	craigslistOxBrowserFetch = stubOx403 // shouldn't be called

	results, err := SearchCraigslistJobs(context.Background(), "warehouse", "sfbay", 100)
	if err != nil {
		t.Fatalf("expected nil error for genuine empty feed, got: %v", err)
	}
	if results != nil {
		t.Errorf("expected nil results for genuine empty feed, got %d results", len(results))
	}
}

// TestResolveRegion_SFBayVariants verifies that the three location strings
// from the task all resolve to the sfbay slug.
func TestResolveRegion_SFBayVariants(t *testing.T) {
	cases := []string{
		"San Francisco Bay Area",
		"San Francisco, CA",
		"SF Bay",
	}
	for _, loc := range cases {
		got := resolveRegion(loc)
		if got != craigslistCitySFBay {
			t.Errorf("resolveRegion(%q) = %q, want %q", loc, got, craigslistCitySFBay)
		}
	}
}

// TestSearchCraigslistJobs_StealthSuccess_NoOxBrowserCall verifies that when
// stealth succeeds, the ox-browser tier is never called.
func TestSearchCraigslistJobs_StealthSuccess_NoOxBrowserCall(t *testing.T) {
	saveFetchVars(t)

	fixture, err := os.ReadFile("testdata/craigslist_rss_real.xml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	oxCalled := false
	craigslistStealthFetch = stubStealthSuccess(fixture)
	craigslistOxBrowserFetch = func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		oxCalled = true
		return http.StatusOK, nil, nil
	}

	results, err := SearchCraigslistJobs(context.Background(), "warehouse", "sfbay", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results from stealth tier")
	}
	if oxCalled {
		t.Error("ox-browser tier was called even though stealth succeeded — should short-circuit")
	}
}

// TestParseCraigslistRSS_EmptyRDF verifies that an empty RDF feed (no items)
// returns (nil, nil) without error.
func TestParseCraigslistRSS_EmptyRDF(t *testing.T) {
	emptyRDF := `<?xml version="1.0" encoding="UTF-8"?>
<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns="http://purl.org/rss/1.0/" xmlns:dc="http://purl.org/dc/elements/1.1/">
<channel rdf:about="https://sfbay.craigslist.org/search/jjj?format=rss">
<title>craigslist SF bay area | all jobs search </title>
<link>https://sfbay.craigslist.org/search/jjj</link>
<description></description>
<items><rdf:Seq></rdf:Seq></items>
</channel>
</rdf:RDF>`

	results, err := parseCraigslistRSS([]byte(emptyRDF), 100)
	if err != nil {
		t.Fatalf("parse error on empty RDF: %v", err)
	}
	if results != nil {
		t.Errorf("expected nil results for empty RDF, got %d", len(results))
	}
}

// TestParseCraigslistRSS_LimitRespected verifies the limit parameter caps results.
func TestParseCraigslistRSS_LimitRespected(t *testing.T) {
	body, err := os.ReadFile("testdata/craigslist_rss_real.xml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	results, err := parseCraigslistRSS(body, 5)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(results) > 5 {
		t.Errorf("expected at most 5 results, got %d", len(results))
	}
}

// Ensure engine.Config is initialized for tests that reference engine.Cfg fields.
var _ = engine.Config{}
var _ stealth.BrowserClient
