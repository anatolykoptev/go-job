package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	stealth "github.com/anatolykoptev/go-stealth"
	"github.com/anatolykoptev/go_job/internal/dbtest"
	"github.com/anatolykoptev/go_job/internal/engine"
)

// --- Test helpers ---

// saveFetchVars saves the current fetch function variables and restores them on cleanup.
func saveFetchVars(t *testing.T) {
	t.Helper()
	origStealth := craigslistStealthFetch
	origOxFetch := craigslistOxFetchFetch
	origOx := craigslistOxBrowserFetch
	origOxURL := engine.Cfg.OxBrowserURL
	origHTTPClient := engine.Cfg.HTTPClient
	// H4: craigslistOxFetchFetch now routes through engine.Cfg.HTTPClient
	// (PF-13 connection-pooled client) instead of http.DefaultClient. The
	// classification tests below exercise the REAL craigslistOxFetchFetch body
	// against an httptest server, so HTTPClient must be non-nil. Production
	// engine.Init always sets it; tests don't call Init, so seed a plain client.
	if engine.Cfg.HTTPClient == nil {
		engine.Cfg.HTTPClient = &http.Client{}
	}
	t.Cleanup(func() {
		craigslistStealthFetch = origStealth
		craigslistOxFetchFetch = origOxFetch
		craigslistOxBrowserFetch = origOx
		engine.Cfg.OxBrowserURL = origOxURL
		engine.Cfg.HTTPClient = origHTTPClient
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

// stubOxFetchSuccess returns the given body with status 200 (simulates a
// successful ox-browser /fetch response — wrapper 200, inner 200, body present).
func stubOxFetchSuccess(body []byte) func(context.Context, string, map[string]string) (int, []byte, error) {
	return func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		return http.StatusOK, body, nil
	}
}

// stubOx403 returns a 403 — both tiers refused (used for RSS tier stub).
func stubOx403(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
	return http.StatusForbidden, []byte("blocked"), nil
}

// stubOxError returns a transport error (used for RSS tier stub).
func stubOxError(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
	return 0, nil, errors.New("ox-browser: connection refused")
}

// --- HTML parser tests ---

// TestParseCraigslistHTML_PopulatedPage (test 1): a real captured populated
// Craigslist search page (cat=jjj, query=warehouse, sfbay) with 112 listings.
// Captured live from ox-browser POST /fetch on 2026-07-28. Must parse to N
// results with title, URL, location, entity-decoded.
func TestParseCraigslistHTML_PopulatedPage(t *testing.T) {
	body, err := os.ReadFile("testdata/craigslist_html_jjj_warehouse.html")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	results, err := parseCraigslistHTML(body, 100)
	if err != nil {
		t.Fatalf("parseCraigslistHTML failed on real fixture: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("parseCraigslistHTML returned 0 results from a real 112-item fixture")
	}

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
	if first.Metadata["location"] == "" {
		t.Errorf("first result missing location metadata: %+v", first.Metadata)
	}
	if !strings.Contains(first.Content, "Location:") {
		t.Errorf("first result content missing location: %s", first.Content)
	}
	// Entity decoding: html.Parse decodes entities in text nodes and attributes.
	for i, r := range results {
		if strings.Contains(r.Title, "&amp;") {
			t.Errorf("result %d title has undecoded &amp;: %s", i, r.Title)
		}
		if strings.Contains(r.Title, "&#39;") {
			t.Errorf("result %d title has undecoded &#39;: %s", i, r.Title)
		}
	}
	t.Logf("parsed %d results from real fixture; first: title=%q url=%q loc=%q",
		len(results), first.Title, first.URL, first.Metadata["location"])
}

// TestParseCraigslistHTML_ZeroResultPage (test 2): a real captured zero-result
// Craigslist search page (cat=jjj, query=zzqqxxnothingmatches12345, sfbay).
// The page has <ol class="cl-static-search-results"> with zero
// li.cl-static-search-result children (it has li.cl-static-hub-links instead).
// Must return (nil, nil) — genuine empty, NOT ErrParse, NOT discovery fallback.
//
// MUTATION-CHECK: if the genuine-empty branch (len(results)==0 → return nil,nil)
// is reverted to return ErrParse, this test goes red.
func TestParseCraigslistHTML_ZeroResultPage(t *testing.T) {
	body, err := os.ReadFile("testdata/craigslist_html_jjj_zero.html")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	results, err := parseCraigslistHTML(body, 100)
	if err != nil {
		t.Fatalf("parseCraigslistHTML on zero-result page must return (nil, nil), got error: %v", err)
	}
	if results != nil {
		t.Errorf("expected nil results for genuine empty page, got %d results", len(results))
	}
}

// TestParseCraigslistHTML_NoResultsOL_ReturnsErrParse (test 3): a body with no
// <ol class="cl-static-search-results"> is not a Craigslist search page →
// ErrParse, NOT (nil, nil).
func TestParseCraigslistHTML_NoResultsOL_ReturnsErrParse(t *testing.T) {
	body := []byte(`<!DOCTYPE html><html><head><title>Are you human?</title></head>
<body><div class="challenge">verify you are not a bot</div></body></html>`)

	results, err := parseCraigslistHTML(body, 100)
	if err == nil {
		t.Fatal("expected ErrParse for non-Craigslist page, got nil error")
	}
	if results != nil {
		t.Errorf("expected nil results for ErrParse, got %d", len(results))
	}
	if !errors.Is(err, ErrParse) {
		t.Errorf("error does not wrap ErrParse: %v", err)
	}
}

// TestParseCraigslistHTML_EntityDecoding (test 7): a title containing &amp;
// and &#x0024; must be decoded in the returned result. html.Parse decodes
// entities in text nodes and attributes automatically.
func TestParseCraigslistHTML_EntityDecoding(t *testing.T) {
	body := []byte(`<ol class="cl-static-search-results">
<li class="cl-static-search-result" title="Warehouse &amp; Delivery &#x0024;25/hr">
<a href="https://www.craigslist.org/view/d/test-warehouse/abc123"><div class="title">Warehouse &amp; Delivery &#x0024;25/hr</div><div class="details"><div class="price">$0</div><div class="location">downtown</div></div></a>
</li>
</ol>`)

	results, err := parseCraigslistHTML(body, 100)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	want := "Warehouse & Delivery $25/hr"
	if results[0].Title != want {
		t.Errorf("title not decoded:\n  got:  %q\n  want: %q", results[0].Title, want)
	}
}

// TestParseCraigslistHTML_LimitRespected verifies the limit parameter caps results.
func TestParseCraigslistHTML_LimitRespected(t *testing.T) {
	body, err := os.ReadFile("testdata/craigslist_html_jjj_warehouse.html")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	results, err := parseCraigslistHTML(body, 5)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(results) > 5 {
		t.Errorf("expected at most 5 results, got %d", len(results))
	}
}

// TestParseCraigslistHTML_SubstringTrap verifies that the parser does NOT
// false-positive on the CSS literal "cl-static-search-result" that appears 3
// times in the page's own CSS even on a zero-result page. A substring count
// would return 3; the token-based hasClassToken must return 0 elements.
func TestParseCraigslistHTML_SubstringTrap(t *testing.T) {
	body, err := os.ReadFile("testdata/craigslist_html_jjj_zero.html")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	results, err := parseCraigslistHTML(body, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results != nil {
		t.Errorf("substring trap: zero-result page must return nil, got %d results (hasClassToken is matching CSS text, not elements)", len(results))
	}
}

// --- RSS parser tests ---

// TestParseCraigslistRSS_RealFixture validates the RSS parser against a REAL
// captured Craigslist RSS feed (RDF/XML, RSS 1.0 format).
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
		t.Fatal("parseCraigslistRSS returned 0 results from a real 23-item fixture")
	}

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
	if !strings.Contains(first.Content, "Craigslist") {
		t.Errorf("first result content missing source tag: %s", first.Content)
	}
	if !strings.Contains(first.Content, "Posted:") {
		t.Errorf("first result content missing posted date: %s", first.Content)
	}
	if strings.Contains(first.Title, "&#x0024;") {
		t.Errorf("first result title has undecoded &#x0024;: %s", first.Title)
	}
	if !strings.Contains(first.Title, "$") {
		t.Errorf("first result title missing decoded $ sign: %s", first.Title)
	}
	if strings.Contains(first.Content, "<br") || strings.Contains(first.Content, "<p>") {
		t.Errorf("first result content has unstripped HTML tags: %s", first.Content)
	}
	t.Logf("parsed %d results; first title=%q", len(results), first.Title)
}

// TestParseCraigslistRSS_EntityDecoding is test 5 for the RSS path.
func TestParseCraigslistRSS_EntityDecoding(t *testing.T) {
	rdf := `<?xml version="1.0" encoding="UTF-8"?>
<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns="http://purl.org/rss/1.0/" xmlns:dc="http://purl.org/dc/elements/1.1/">
<channel rdf:about="https://sfbay.craigslist.org/search/jjj?format=rss"><title>x</title><link>x</link><description></description><items><rdf:Seq></rdf:Seq></items></channel>
<item rdf:about="https://sfbay.craigslist.org/sfc/sof/d/test/123.html">
<title><![CDATA[Warehouse &amp; Delivery &#x0024;25/hr]]></title>
<link>https://sfbay.craigslist.org/sfc/sof/d/test/123.html</link>
<description><![CDATA[<p>Great <b>job</b> &amp; benefits</p>]]></description>
<dc:date>2026-07-28T10:00:00-07:00</dc:date>
</item>
</rdf:RDF>`

	results, err := parseCraigslistRSS([]byte(rdf), 100)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	wantTitle := "Warehouse & Delivery $25/hr"
	if results[0].Title != wantTitle {
		t.Errorf("RSS title not decoded:\n  got:  %q\n  want: %q", results[0].Title, wantTitle)
	}
	if strings.Contains(results[0].Content, "<p>") || strings.Contains(results[0].Content, "<b>") {
		t.Errorf("RSS description has unstripped tags: %s", results[0].Content)
	}
	if strings.Contains(results[0].Content, "&amp;") {
		t.Errorf("RSS description has undecoded &amp;: %s", results[0].Content)
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

// --- ox-browser /fetch classification tests (tests 4, 5, 6) ---
//
// These tests use httptest.Server to serve a FAKE ox-browser /fetch endpoint,
// so the REAL craigslistOxFetchFetch classification code processes the response.
// Stubbing the function variable directly would bypass the classification
// logic — the exact defect the reviewer found (test feeds a shape production
// cannot produce).

// oxFetchTestServer starts an httptest server that responds to POST /fetch
// with the given wrapper status code and oxFetchResponse JSON body.
func oxFetchTestServer(t *testing.T, wrapperStatus int, oxResp oxFetchResponse) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(wrapperStatus)
		body, _ := json.Marshal(oxResp)
		_, _ = w.Write(body)
	}))
}

// TestSearchCraigslistJobs_OxFetchInner403_Blocked (test 4a): ox-browser /fetch
// returns wrapper 200 with inner status 403 → errCraigslistBlocked.
func TestSearchCraigslistJobs_OxFetchInner403_Blocked(t *testing.T) {
	saveFetchVars(t)

	srv := oxFetchTestServer(t, http.StatusOK, oxFetchResponse{
		Status: http.StatusForbidden,
		Body:   "",
	})
	defer srv.Close()
	engine.Cfg.OxBrowserURL = srv.URL

	craigslistStealthFetch = stubStealth403
	craigslistOxBrowserFetch = stubOx403

	_, err := SearchCraigslistJobs(context.Background(), "warehouse", "sfbay", 100)
	if err == nil {
		t.Fatal("expected blocked error, got nil")
	}
	if !errors.Is(err, errCraigslistBlocked) {
		t.Errorf("expected errCraigslistBlocked, got: %v", err)
	}
}

// TestSearchCraigslistJobs_OxFetchCFDetected_Blocked (test 4b): ox-browser /fetch
// returns wrapper 200 with cf_detected=true → errCraigslistBlocked.
func TestSearchCraigslistJobs_OxFetchCFDetected_Blocked(t *testing.T) {
	saveFetchVars(t)

	srv := oxFetchTestServer(t, http.StatusOK, oxFetchResponse{
		Status:     http.StatusOK,
		Body:       "<html>cf challenge</html>",
		CfDetected: true,
	})
	defer srv.Close()
	engine.Cfg.OxBrowserURL = srv.URL

	craigslistStealthFetch = stubStealth403
	craigslistOxBrowserFetch = stubOx403

	_, err := SearchCraigslistJobs(context.Background(), "warehouse", "sfbay", 100)
	if err == nil {
		t.Fatal("expected blocked error, got nil")
	}
	if !errors.Is(err, errCraigslistBlocked) {
		t.Errorf("expected errCraigslistBlocked, got: %v", err)
	}
}

// TestSearchCraigslistJobs_OxFetchSolverCascade_Blocked (test 5): ox-browser
// /fetch returns wrapper 502 with a solver/cf_clearance cascade error →
// errCraigslistBlocked. This is the real production block signature: ox-browser
// absorbs 403, escalates to its proxy pool and CF solver, and on exhaustion
// returns wrapper 502 / status 0 / "proxy pool error: solver failed: ...".
func TestSearchCraigslistJobs_OxFetchSolverCascade_Blocked(t *testing.T) {
	saveFetchVars(t)

	srv := oxFetchTestServer(t, http.StatusBadGateway, oxFetchResponse{
		Status: 0,
		Error:  "proxy pool error: solver failed: timeout waiting for cf_clearance",
	})
	defer srv.Close()
	engine.Cfg.OxBrowserURL = srv.URL

	craigslistStealthFetch = stubStealth403
	craigslistOxBrowserFetch = stubOx403

	_, err := SearchCraigslistJobs(context.Background(), "warehouse", "sfbay", 100)
	if err == nil {
		t.Fatal("expected blocked error, got nil")
	}
	if !errors.Is(err, errCraigslistBlocked) {
		t.Errorf("expected errCraigslistBlocked, got: %v", err)
	}
}

// TestSearchCraigslistJobs_OxFetchConnectError_NotBlocked (test 6): ox-browser
// /fetch returns wrapper 502 with a connect error (NOT a solver cascade) → the
// returned error carries the underlying cause, and errors.Is(err,
// errCraigslistBlocked) is FALSE. A connect error is not a block;
// context.DeadlineExceeded must still reach engine.PlatformOutcome as
// outcome=timeout.
//
// MUTATION-CHECK: if the connect-error branch is changed to return
// errCraigslistBlocked, this test goes red.
func TestSearchCraigslistJobs_OxFetchConnectError_NotBlocked(t *testing.T) {
	saveFetchVars(t)

	srv := oxFetchTestServer(t, http.StatusBadGateway, oxFetchResponse{
		Status: 0,
		Error:  "request failed: error sending request for uri (https://...): client error (Connect)",
	})
	defer srv.Close()
	engine.Cfg.OxBrowserURL = srv.URL

	stealthErr := errors.New("stealth: dial timeout")
	craigslistStealthFetch = func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		return 0, nil, stealthErr
	}
	craigslistOxBrowserFetch = stubOxError

	_, err := SearchCraigslistJobs(context.Background(), "warehouse", "sfbay", 100)
	if err == nil {
		t.Fatal("expected error when all tiers fail with transport errors, got nil")
	}
	if errors.Is(err, errCraigslistBlocked) {
		t.Errorf("connect errors must NOT be classified as blocked: %v", err)
	}
	if !strings.Contains(err.Error(), "dial timeout") && !strings.Contains(err.Error(), "Connect") {
		t.Errorf("returned error does not carry the underlying cause: %v", err)
	}
}

// --- isOxBrowserCascadeError unit tests ---

// TestIsOxBrowserCascadeError verifies the substring match against ox-browser's
// error strings (source: ox-browser crates/http/src/middleware_solver.rs:107,122
// and error.rs:19 HttpError::ProxyPool).
func TestIsOxBrowserCascadeError(t *testing.T) {
	cases := []struct {
		err  string
		want bool
		why  string
	}{
		{"proxy pool error: solver failed: timeout waiting for cf_clearance", true, "solver cascade (row 5)"},
		{"proxy pool error: solver failed: navigate: navigation failed: net::ERR_HTTP_RESPONSE_CODE_FAILURE", true, "solver cascade (row 6)"},
		{"proxy pool error: solver negcache: domain craigslist.org on cooldown", true, "solver negcache cooldown"},
		{"request failed: error sending request for uri (https://...): client error (Connect)", false, "connect error (row 7)"},
		{"timeout after 30s", false, "timeout"},
		{"invalid URL: bad scheme", false, "client error"},
		{"", false, "empty error"},
	}
	for _, c := range cases {
		got := isOxBrowserCascadeError(c.err)
		if got != c.want {
			t.Errorf("isOxBrowserCascadeError(%q) = %v, want %v (%s)", c.err, got, c.want, c.why)
		}
	}
}

// --- Ladder / SearchCraigslistJobs tests ---

// TestSearchCraigslistJobs_Stealth403OxFetchSuccess verifies the two-tier
// escalation: stealth returns 403, ox-browser /fetch returns the page → results.
func TestSearchCraigslistJobs_Stealth403OxFetchSuccess(t *testing.T) {
	saveFetchVars(t)
	engine.Cfg.OxBrowserURL = "http://ox-browser-test:8901"

	fixture, err := os.ReadFile("testdata/craigslist_html_jjj_warehouse.html")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	craigslistStealthFetch = stubStealth403
	craigslistOxFetchFetch = stubOxFetchSuccess(fixture)
	craigslistOxBrowserFetch = stubOx403

	results, err := SearchCraigslistJobs(context.Background(), "warehouse", "sfbay", 100)
	if err != nil {
		t.Fatalf("expected results, got error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results from ox-browser /fetch fallback, got 0 — escalation did not fire")
	}
}

// TestSearchCraigslistJobs_AllTiersBlocked_ReturnsBlockedError: all tiers
// refused → blocked error, NOT (nil, nil).
func TestSearchCraigslistJobs_AllTiersBlocked_ReturnsBlockedError(t *testing.T) {
	saveFetchVars(t)

	srv := oxFetchTestServer(t, http.StatusOK, oxFetchResponse{
		Status: http.StatusForbidden,
	})
	defer srv.Close()
	engine.Cfg.OxBrowserURL = srv.URL

	craigslistStealthFetch = stubStealth403
	craigslistOxBrowserFetch = stubOx403

	results, err := SearchCraigslistJobs(context.Background(), "warehouse", "sfbay", 100)
	if err == nil {
		t.Fatal("expected blocked error, got nil — this is the shipped defect (nil, nil → outcome=empty)")
	}
	if results != nil {
		t.Errorf("expected nil results on blocked, got %d results", len(results))
	}
	if !errors.Is(err, errCraigslistBlocked) {
		t.Errorf("expected errCraigslistBlocked, got: %v", err)
	}
}

// TestSearchCraigslistJobs_Tier1ChallengeEscalates: HTTP 200 with a challenge
// body on tier 1 → escalates to tier 2. A 200 carrying a challenge page has no
// ol.cl-static-search-results, so the parser returns ErrParse; the ladder
// treats this as a soft-block signal and escalates.
func TestSearchCraigslistJobs_Tier1ChallengeEscalates(t *testing.T) {
	saveFetchVars(t)
	engine.Cfg.OxBrowserURL = "http://ox-browser-test:8901"

	challenge := []byte(`<!DOCTYPE html><html><head><title>Are you human?</title></head><body><div class="challenge">verify you are not a bot</div></body></html>`)
	fixture, err := os.ReadFile("testdata/craigslist_html_jjj_warehouse.html")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	craigslistStealthFetch = stubStealthSuccess(challenge)
	craigslistOxFetchFetch = stubOxFetchSuccess(fixture)
	craigslistOxBrowserFetch = stubOx403

	results, err := SearchCraigslistJobs(context.Background(), "warehouse", "sfbay", 100)
	if err != nil {
		t.Fatalf("expected escalation to tier 2 to yield results, got error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("tier 1 challenge did not escalate to tier 2 — got 0 results")
	}
}

// TestSearchCraigslistJobs_GenuineEmpty_ReturnsNil: stealth returns the
// zero-result page → parser returns (nil, nil) → connector returns (nil, nil),
// NOT discovery-fallback listings. This is the defect the reviewer found:
// a zero-match query fell through to discovery and returned unrelated listings
// as outcome=ok.
func TestSearchCraigslistJobs_GenuineEmpty_ReturnsNil(t *testing.T) {
	saveFetchVars(t)
	engine.Cfg.OxBrowserURL = "http://ox-browser-test:8901"

	fixture, err := os.ReadFile("testdata/craigslist_html_jjj_zero.html")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	craigslistStealthFetch = stubStealthSuccess(fixture)
	craigslistOxFetchFetch = stubOxFetchSuccess(fixture)
	craigslistOxBrowserFetch = stubOx403

	results, err := SearchCraigslistJobs(context.Background(), "zzqqxxnothingmatches12345", "sfbay", 100)
	if err != nil {
		t.Fatalf("expected nil error for genuine empty, got: %v", err)
	}
	if results != nil {
		t.Errorf("expected nil results for genuine empty, got %d results (discovery fallback laundering)", len(results))
	}
}

// TestSearchCraigslistJobs_StealthSuccess_NoEscalation verifies that when
// stealth HTML tier-1 succeeds, the ox-browser tier is never called.
func TestSearchCraigslistJobs_StealthSuccess_NoEscalation(t *testing.T) {
	saveFetchVars(t)
	engine.Cfg.OxBrowserURL = "http://ox-browser-test:8901"

	fixture, err := os.ReadFile("testdata/craigslist_html_jjj_warehouse.html")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	oxFetchCalled := false
	oxCalled := false
	craigslistStealthFetch = stubStealthSuccess(fixture)
	craigslistOxFetchFetch = func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		oxFetchCalled = true
		return http.StatusOK, nil, nil
	}
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
	if oxFetchCalled {
		t.Error("ox-browser /fetch tier was called even though stealth succeeded — should short-circuit")
	}
	if oxCalled {
		t.Error("ox-browser RSS tier was called even though stealth succeeded — should short-circuit")
	}
}

// TestSearchCraigslistJobs_OxBrowserURLEmpty_SkipsTier2 verifies that when
// OxBrowserURL is empty, tier 2 is skipped (and the skip is reported via log).
func TestSearchCraigslistJobs_OxBrowserURLEmpty_SkipsTier2(t *testing.T) {
	saveFetchVars(t)
	engine.Cfg.OxBrowserURL = ""

	fixture, err := os.ReadFile("testdata/craigslist_html_jjj_warehouse.html")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	oxFetchCalled := false
	craigslistStealthFetch = stubStealthSuccess(fixture)
	craigslistOxFetchFetch = func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		oxFetchCalled = true
		return http.StatusOK, nil, nil
	}
	craigslistOxBrowserFetch = stubOx403

	results, err := SearchCraigslistJobs(context.Background(), "warehouse", "sfbay", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results from stealth tier")
	}
	if oxFetchCalled {
		t.Error("ox-browser /fetch tier was called even though OxBrowserURL is empty — should be skipped")
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
		got, ok := resolveRegion(loc)
		if !ok || got != craigslistCitySFBay {
			t.Errorf("resolveRegion(%q) = (%q, %v), want (%q, true)", loc, got, ok, craigslistCitySFBay)
		}
	}
}

// TestResolveRegion_UnmappedLocationsNotWWW (H1): the sentinel "www" must NOT
// reach the URL path segment. Unmapped locations return ("", false) so the
// caller fails the direct tiers with a named error instead of building a
// /search/area/www?... URL that 404s (measured live: /search/area/www → 404).
// "Portland, OR" resolves via the substring pass (ok=true) and MUST produce a
// searchable area slug — the sentinel never reaches the path.
//
// NOTE: which area "Portland, OR" resolves to is non-deterministic (#347 —
// "portland, or" contains the substring "la", so map iteration may match
// "losangeles" before "portland"). #347 is out of scope; this test asserts
// ONLY the H1 invariant (no "www" sentinel, ok=true for mapped-via-substring
// inputs, ok=false for genuinely unmapped inputs), not a specific region.
//
// MUTATION-CHECK: revert resolveRegion to return ("www", true) for unmatched →
// the unmapped cases fail (ok=true, region="www") and craigslistHTMLSearchURL
// produces /area/www → red.
func TestResolveRegion_UnmappedLocationsNotWWW(t *testing.T) {
	unmapped := []string{"", "Remote", "Berlin"}
	for _, loc := range unmapped {
		region, ok := resolveRegion(loc)
		if ok {
			t.Errorf("resolveRegion(%q): ok=true (region=%q), want false — unmapped location must not produce a region", loc, region)
			continue
		}
		if region != "" {
			t.Errorf("resolveRegion(%q): region=%q on miss, want empty", loc, region)
		}
	}

	// "Portland, OR" matches via the substring pass (ok=true). Assert the
	// sentinel never reaches the path and the URL is a searchable area — but
	// NOT which area (#347 non-determinism).
	region, ok := resolveRegion("Portland, OR")
	if !ok {
		t.Fatalf("resolveRegion(%q): ok=false, want true — must match via substring pass", "Portland, OR")
	}
	if region == "www" {
		t.Fatalf("resolveRegion(%q): returned sentinel \"www\" — must not reach the URL path", "Portland, OR")
	}
	u := craigslistHTMLSearchURL(region, "warehouse")
	if strings.Contains(u, "/area/www") {
		t.Errorf("resolveRegion(%q): built URL contains /area/www: %s", "Portland, OR", u)
	}
	if !strings.Contains(u, "/area/"+region) {
		t.Errorf("resolveRegion(%q): built URL missing /area/%s: %s", "Portland, OR", region, u)
	}
}

// TestSynthesizeLadderError_BlockedPreservesDeadlineAnd404 (H3): when any tier
// was refused (block) AND another tier had a transport error, the returned
// error must wrap errCraigslistBlocked AND preserve the underlying causes
// (context.DeadlineExceeded, a 404 message). The previous code returned the
// bare sentinel, dropping errs — so errors.Is(err, context.DeadlineExceeded)
// was FALSE (PlatformOutcome reported "error" not "timeout") and a 404 cause
// was absent (a URL-construction bug was indistinguishable from an IP block).
//
// MUTATION-CHECK: revert synthesizeLadderError to `return nil, errCraigslistBlocked`
// on anyRefused → errors.Is(err, context.DeadlineExceeded) becomes FALSE and the
// 404 string is absent → red.
func TestSynthesizeLadderError_BlockedPreservesDeadlineAnd404(t *testing.T) {
	outcomes := []tierOutcome{
		{name: "html-static", err: errors.New("craigslist html-static refused: HTTP 404"), refused: true},
		{name: "ox-fetch", err: fmt.Errorf("craigslist ox-fetch: %w", context.DeadlineExceeded), refused: false},
	}
	_, err := synthesizeLadderError(outcomes)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, errCraigslistBlocked) {
		t.Errorf("error must still wrap errCraigslistBlocked: %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error must preserve context.DeadlineExceeded (PlatformOutcome needs outcome=timeout): %v", err)
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error message must carry the 404 cause (URL-construction bug must be distinguishable from IP block): %v", err)
	}
}

// --- MAJOR 1: caller-level resolveRegion guard coverage ---
//
// The pure helper resolveRegion is tested above, but the invariant that
// actually ships is "no empty or sentinel region reaches /search/area/<region>"
// — and that lives in the CALLERS' `if !ok` guards. Deleting both guards
// (mutation M1b) leaves the entire ./internal/engine/jobs/ suite GREEN because
// no test exercises the caller path with an unmapped location. These two
// tests close that gap: an unmapped location must produce an error containing
// "not mapped" AND must not invoke ANY transport seam (the guard returns
// before any fetch).

// transportCalledStub returns a transport seam function that fails the test if
// invoked — the guard must return before any transport call.
func transportCalledStub(t *testing.T, name string) func(context.Context, string, map[string]string) (int, []byte, error) {
	t.Helper()
	return func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		t.Errorf("transport seam %s was invoked for an unmapped location — the resolveRegion guard was deleted", name)
		return 0, nil, errors.New("transport should not have been called")
	}
}

// TestFetchCraigslistListings_UnmappedLocation_NoTransportCall (MAJOR 1):
// fetchCraigslistListings with an unmapped location must return an error
// containing "not mapped" and must NOT invoke any transport seam.
//
// MUTATION-CHECK: delete the `if !ok` guard at the top of
// fetchCraigslistListings (replacing `ok` with `_`) → region="" → the function
// builds a URL and calls craigslistStealthFetch → the transport stub fires →
// RED. Also the error no longer contains "not mapped" → RED.
func TestFetchCraigslistListings_UnmappedLocation_NoTransportCall(t *testing.T) {
	saveFetchVars(t)
	engine.Cfg.OxBrowserURL = "http://ox-browser-test:8901"

	craigslistStealthFetch = transportCalledStub(t, "stealth")
	craigslistOxFetchFetch = transportCalledStub(t, "ox-fetch")
	craigslistOxBrowserFetch = transportCalledStub(t, "ox-browser")

	_, err := fetchCraigslistListings(context.Background(), "warehouse", "Berlin", 1)
	if err == nil {
		t.Fatal("expected error for unmapped location, got nil — the resolveRegion guard was deleted")
	}
	if !strings.Contains(err.Error(), "not mapped") {
		t.Errorf("error must contain \"not mapped\", got: %v", err)
	}
	if !errors.Is(err, errCraigslistUnmapped) {
		t.Errorf("error must wrap errCraigslistUnmapped, got: %v", err)
	}
}

// TestFetchCraigslistRSS_UnmappedLocation_NoTransportCall (MAJOR 1):
// fetchCraigslistRSS with an unmapped location must return an error containing
// "not mapped" and must NOT invoke any transport seam.
//
// MUTATION-CHECK: delete the `if !ok` guard at the top of fetchCraigslistRSS
// → region="" → the function builds a URL and calls craigslistStealthFetch →
// the transport stub fires → RED.
func TestFetchCraigslistRSS_UnmappedLocation_NoTransportCall(t *testing.T) {
	saveFetchVars(t)

	craigslistStealthFetch = transportCalledStub(t, "stealth")
	craigslistOxBrowserFetch = transportCalledStub(t, "ox-browser")

	_, err := fetchCraigslistRSS(context.Background(), "warehouse", "Berlin", 1)
	if err == nil {
		t.Fatal("expected error for unmapped location, got nil — the resolveRegion guard was deleted")
	}
	if !strings.Contains(err.Error(), "not mapped") {
		t.Errorf("error must contain \"not mapped\", got: %v", err)
	}
	if !errors.Is(err, errCraigslistUnmapped) {
		t.Errorf("error must wrap errCraigslistUnmapped, got: %v", err)
	}
}

// --- MINOR 1: H2 counter increment coverage ---
//
// The IncrCraigslistDiscoveryFallback call at the discovery-fallback success
// path is itself unguarded by a test (mutation M2: delete the line → GREEN).
// This test stubs the direct tiers to fail and the ATS discoverer to return
// craigslist URLs, then asserts the counter incremented.

// mockATSDiscoverer returns a fixed set of results for any query.
type mockATSDiscoverer struct {
	results []engine.SearxngResult
}

func (m *mockATSDiscoverer) DiscoverBoardURLs(_ context.Context, _ string) ([]engine.SearxngResult, error) {
	return m.results, nil
}

// TestSearchCraigslistJobs_DiscoveryFallback_IncrsCounter (MINOR 1):
// when the direct tiers fail and the discovery fallback serves results, the
// craigslist_discovery_fallback_total{reason} counter MUST increment.
//
// MUTATION-CHECK: delete the `engine.IncrCraigslistDiscoveryFallback(reason)`
// line in SearchCraigslistJobs → the counter delta is 0 → RED.
func TestSearchCraigslistJobs_DiscoveryFallback_IncrsCounter(t *testing.T) {
	saveFetchVars(t)
	engine.Cfg.OxBrowserURL = "http://ox-browser-test:8901"

	// Direct tiers: all fail (stealth 403, ox-fetch 403, RSS 403).
	craigslistStealthFetch = stubStealth403
	craigslistOxFetchFetch = stubOx403
	craigslistOxBrowserFetch = stubOx403

	// Discovery: return a craigslist job URL so the fallback serves results.
	origATS := ATSDiscoverer
	t.Cleanup(func() { ATSDiscoverer = origATS })
	SetATSDiscoverer(&mockATSDiscoverer{
		results: []engine.SearxngResult{
			{Title: "Warehouse Worker", URL: "https://sfbay.craigslist.org/sfc/sof/d/test-warehouse/123.html"},
		},
	})

	// Snapshot the counter before.
	key := engine.MetricCraigslistDiscoveryFallback + "{reason=blocked}"
	before := engine.GetMetrics()[key]

	_, err := SearchCraigslistJobs(context.Background(), "warehouse", "sfbay", 100)
	if err != nil {
		t.Fatalf("expected discovery fallback to serve results, got error: %v", err)
	}

	after := engine.GetMetrics()[key]
	if after <= before {
		t.Errorf("craigslist_discovery_fallback_total{reason=blocked} did not increment (before=%d after=%d) — IncrCraigslistDiscoveryFallback call was deleted", before, after)
	}
}

// Ensure engine.Config is initialized for tests that reference engine.Cfg fields.
var _ = engine.Config{}
var _ stealth.BrowserClient

// --- Default-location fallback (F1/F2/F3) ---
//
// Craigslist is region-scoped: an empty location has nowhere to go. When the
// caller supplies none, the connector resolves one from (1) the operator's
// resume profile, then (2) engine.Cfg.CraigslistDefaultLocation, and only if
// both are empty keeps the errCraigslistUnmapped error. An explicit location
// always wins and an unmappable explicit location always errors — silently
// searching the wrong city is worse than failing.

// saveDefaultLocationDeps saves/restores the craigslistProfileLocation seam,
// the CraigslistDefaultLocation config field, and the profile-location cache
// so the F1-F6 tests are isolated from each other and from prior runs.
func saveDefaultLocationDeps(t *testing.T) {
	t.Helper()
	origProfile := craigslistProfileLocation
	origDefault := engine.Cfg.CraigslistDefaultLocation
	origTimeout := craigslistProfileTimeout
	profileLocationCacheMu.Lock()
	origCacheHit := profileLocationCacheHit
	origCacheVal := profileLocationCached
	profileLocationCacheHit = false
	profileLocationCached = ""
	profileLocationCacheMu.Unlock()
	t.Cleanup(func() {
		craigslistProfileLocation = origProfile
		engine.Cfg.CraigslistDefaultLocation = origDefault
		craigslistProfileTimeout = origTimeout
		profileLocationCacheMu.Lock()
		profileLocationCacheHit = origCacheHit
		profileLocationCached = origCacheVal
		profileLocationCacheMu.Unlock()
	})
}

// urlCapturingStealth returns a stealth seam that records the URL it was called
// with and returns the given body as a successful 200 response.
func urlCapturingStealth(captured *string, body []byte) func(context.Context, string, map[string]string) (int, []byte, error) {
	return func(_ context.Context, feedURL string, _ map[string]string) (int, []byte, error) {
		*captured = feedURL
		return http.StatusOK, body, nil
	}
}

// F1 — profile fallback: a search with NO explicit location, against a profile
// whose location is set, must resolve the profile's region and return results
// instead of errCraigslistUnmapped.
//
// MUTATION-CHECK: remove the fallback call in SearchCraigslistJobs (leave
// location="" ) → resolveRegion("") returns false → errCraigslistUnmapped →
// the test expects results and gets an error → RED.
func TestSearchCraigslistJobs_EmptyLocation_UsesProfileFallback(t *testing.T) {
	saveFetchVars(t)
	saveDefaultLocationDeps(t)
	engine.Cfg.OxBrowserURL = "http://ox-browser-test:8901"
	engine.Cfg.CraigslistDefaultLocation = ""

	// Profile holds the real stored value: "San Francisco Bay Area".
	craigslistProfileLocation = func(_ context.Context) (string, error) {
		return "San Francisco Bay Area", nil
	}

	fixture, err := os.ReadFile("testdata/craigslist_html_jjj_warehouse.html")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var capturedURL string
	craigslistStealthFetch = urlCapturingStealth(&capturedURL, fixture)
	craigslistOxFetchFetch = stubOxFetchSuccess(fixture)
	craigslistOxBrowserFetch = stubOx403

	results, err := SearchCraigslistJobs(context.Background(), "golang", "", 5)
	if err != nil {
		t.Fatalf("expected results via profile fallback, got error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results via profile fallback, got 0")
	}
	// The profile value "San Francisco Bay Area" resolves to the sfbay area
	// slug via resolveRegion's substring pass — prove the profile location
	// flowed through to the URL, not a hardcoded constant.
	if !strings.Contains(capturedURL, "/area/"+craigslistCitySFBay) {
		t.Errorf("profile fallback did not reach the URL: got %s, want /area/%s", capturedURL, craigslistCitySFBay)
	}
}

// F2 — explicit location still wins: a caller passing an explicit location
// different from the profile's must get THAT location, not the profile's.
// This is the guard against silently searching the wrong city.
//
// MUTATION-CHECK: make the fallback override the caller's value (apply
// resolveCraigslistDefaultLocation unconditionally instead of only when
// location=="") → the URL uses the profile's sfbay region instead of
// newyork → RED.
func TestSearchCraigslistJobs_ExplicitLocation_WinsOverProfile(t *testing.T) {
	saveFetchVars(t)
	saveDefaultLocationDeps(t)
	engine.Cfg.OxBrowserURL = "http://ox-browser-test:8901"

	// Profile says SF; caller says New York. Caller must win.
	craigslistProfileLocation = func(_ context.Context) (string, error) {
		return "San Francisco Bay Area", nil
	}

	fixture, err := os.ReadFile("testdata/craigslist_html_jjj_warehouse.html")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var capturedURL string
	craigslistStealthFetch = urlCapturingStealth(&capturedURL, fixture)
	craigslistOxFetchFetch = stubOxFetchSuccess(fixture)
	craigslistOxBrowserFetch = stubOx403

	results, err := SearchCraigslistJobs(context.Background(), "golang", "new york", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for explicit location, got 0")
	}
	if !strings.Contains(capturedURL, "/area/"+craigslistCityNewYork) {
		t.Errorf("explicit location did not win: got %s, want /area/%s (profile must NOT override the caller)", capturedURL, craigslistCityNewYork)
	}
	if strings.Contains(capturedURL, "/area/"+craigslistCitySFBay) {
		t.Errorf("profile fallback overrode the explicit location: URL used sfbay (%s) — silently searching the wrong city", capturedURL)
	}
}

// F3 — an unmappable explicit location still errors: a caller passing a
// location that maps to nothing must get errCraigslistUnmapped, NOT the
// operator's region via the fallback. The fallback only fires on EMPTY input.
//
// MUTATION-CHECK: make the fallback catch an unmappable explicit location
// (fall through to resolveCraigslistDefaultLocation when resolveRegion fails)
// → the profile's sfbay region is used → transport is called and the error is
// no longer errCraigslistUnmapped → RED.
func TestSearchCraigslistJobs_UnmappableExplicitLocation_StillErrors(t *testing.T) {
	saveFetchVars(t)
	saveDefaultLocationDeps(t)
	engine.Cfg.OxBrowserURL = "http://ox-browser-test:8901"

	// Profile is set, but the caller's explicit "Berlin" must NOT fall back to it.
	craigslistProfileLocation = func(_ context.Context) (string, error) {
		return "San Francisco Bay Area", nil
	}

	craigslistStealthFetch = transportCalledStub(t, "stealth")
	craigslistOxFetchFetch = transportCalledStub(t, "ox-fetch")
	craigslistOxBrowserFetch = transportCalledStub(t, "ox-browser")

	_, err := SearchCraigslistJobs(context.Background(), "golang", "Berlin", 5)
	if err == nil {
		t.Fatal("expected errCraigslistUnmapped for unmappable explicit location, got nil — the fallback caught it")
	}
	if !errors.Is(err, errCraigslistUnmapped) {
		t.Errorf("expected errCraigslistUnmapped, got: %v", err)
	}
}

// F1-config — config default fallback: when the profile location is empty but
// engine.Cfg.CraigslistDefaultLocation is set, the config value is used.
//
// MUTATION-CHECK: remove the config-default branch from
// resolveCraigslistDefaultLocation → returns "" → errCraigslistUnmapped → RED.
func TestSearchCraigslistJobs_EmptyLocation_UsesConfigDefault(t *testing.T) {
	saveFetchVars(t)
	saveDefaultLocationDeps(t)
	engine.Cfg.OxBrowserURL = "http://ox-browser-test:8901"

	// No profile location; config default is set.
	craigslistProfileLocation = func(_ context.Context) (string, error) { return "", nil }
	engine.Cfg.CraigslistDefaultLocation = "new york"

	fixture, err := os.ReadFile("testdata/craigslist_html_jjj_warehouse.html")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var capturedURL string
	craigslistStealthFetch = urlCapturingStealth(&capturedURL, fixture)
	craigslistOxFetchFetch = stubOxFetchSuccess(fixture)
	craigslistOxBrowserFetch = stubOx403

	results, err := SearchCraigslistJobs(context.Background(), "golang", "", 5)
	if err != nil {
		t.Fatalf("expected results via config default, got error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results via config default, got 0")
	}
	if !strings.Contains(capturedURL, "/area/"+craigslistCityNewYork) {
		t.Errorf("config default did not reach the URL: got %s, want /area/%s", capturedURL, craigslistCityNewYork)
	}
}

// F1-both-empty — when both the profile and the config default are empty, the
// connector keeps the current errCraigslistUnmapped behaviour.
func TestSearchCraigslistJobs_EmptyLocation_BothEmpty_StillErrors(t *testing.T) {
	saveFetchVars(t)
	saveDefaultLocationDeps(t)
	engine.Cfg.OxBrowserURL = "http://ox-browser-test:8901"

	craigslistProfileLocation = func(_ context.Context) (string, error) { return "", nil }
	engine.Cfg.CraigslistDefaultLocation = ""

	craigslistStealthFetch = transportCalledStub(t, "stealth")
	craigslistOxFetchFetch = transportCalledStub(t, "ox-fetch")
	craigslistOxBrowserFetch = transportCalledStub(t, "ox-browser")

	_, err := SearchCraigslistJobs(context.Background(), "golang", "", 5)
	if err == nil {
		t.Fatal("expected errCraigslistUnmapped when both fallbacks are empty, got nil")
	}
	if !errors.Is(err, errCraigslistUnmapped) {
		t.Errorf("expected errCraigslistUnmapped, got: %v", err)
	}
}

// --- F4: token-boundary matching (#347) ---
//
// resolveRegion's substring pass matched keys INSIDE words: the two-char key
// "la" matched inside "salt", "orlando", "cleveland", "dallas", "portland",
// "atlanta", "oakland", … so an operator in Salt Lake City deterministically
// got Los Angeles jobs with no error and no log. The fix matches on token
// boundaries (split on non-letters) so a key only matches a whole token (or a
// consecutive token run for multi-word keys like "san francisco").
//
// MUTATION-CHECK: restore the bare `strings.Contains(loc, key)` substring pass
// → "Salt Lake City, UT" matches "la" inside "salt" → resolves to losangeles →
// the losangeles-assertion fails → RED. Every row that previously split to
// losangeles must now resolve to its honest region or to ("", false).
//
// Measured on the tip at 400 iterations/input (map order is randomised):
//
//	"Salt Lake City, UT" → losangeles 400/400   (must now be unmapped)
//	"Orlando, FL"        → losangeles 400/400   (must now be unmapped)
//	"Cleveland, OH"      → losangeles 400/400   (must now be unmapped)
//	"Lancaster, PA"      → losangeles 400/400   (must now be unmapped)
//	"Tallahassee, FL"    → losangeles 400/400   (must now be unmapped)
//	"Dallas, TX"         → losangeles 218 / dallas 182  (must now be dallas 400/400)
//	"Atlanta, GA"        → atlanta 215 / losangeles 185 (must now be atlanta 400/400)
//	"Portland, OR"       → portland 209 / losangeles 191 (must now be portland 400/400)
//	"Oakland, CA"        → sfbay 394 / losangeles 6      (must now be sfbay 400/400)
//	"San Francisco Bay Area" → sfbay (positive, must stay sfbay)
func TestResolveRegion_TokenBoundary_NoLosAngelesFalseMatch(t *testing.T) {
	// Cities that must NOT resolve to losangeles. Each either maps to its own
	// region or is honestly unmapped (returns false) — never losangeles.
	mustNotBeLosAngeles := []string{
		"Salt Lake City, UT",
		"Orlando, FL",
		"Cleveland, OH",
		"Lancaster, PA",
		"Tallahassee, FL",
		"Dallas, TX",
		"Atlanta, GA",
		"Portland, OR",
		"Oakland, CA",
	}
	for _, loc := range mustNotBeLosAngeles {
		region, ok := resolveRegion(loc)
		if ok && region == "losangeles" {
			t.Errorf("resolveRegion(%q) = %q — token-boundary fix regressed: \"la\" matched inside a word (was #347)", loc, region)
		}
	}

	// Positive: the cities that DO have a key must resolve to that key's region,
	// deterministically (not losangeles).
	wantRegion := map[string]string{
		"Dallas, TX":   craigslistRegions["dallas"],
		"Atlanta, GA":  craigslistRegions["atlanta"],
		"Portland, OR": craigslistRegions["portland"],
		"Oakland, CA":  craigslistCitySFBay,
	}
	for loc, want := range wantRegion {
		region, ok := resolveRegion(loc)
		if !ok {
			t.Errorf("resolveRegion(%q): ok=false, want %q — a mapped city stopped resolving", loc, want)
			continue
		}
		if region != want {
			t.Errorf("resolveRegion(%q) = %q, want %q", loc, region, want)
		}
	}

	// "San Francisco Bay Area" stays sfbay (matches "san francisco" + "bay area",
	// both → sfbay). Its passing is NOT evidence the matcher is sound — it is
	// asserted here only as a positive so the token-boundary fix does not break a
	// previously-passing input.
	region, ok := resolveRegion("San Francisco Bay Area")
	if !ok || region != craigslistCitySFBay {
		t.Errorf("resolveRegion(%q) = (%q, %v), want (%q, true) — positive case regressed", "San Francisco Bay Area", region, ok, craigslistCitySFBay)
	}
}

// --- F5: determinism ---
//
// A single call proves nothing against Go's randomised map iteration — the
// reviewer needed 400 iterations to see the losangeles/dallas split. The fix
// picks a deterministic winner (longest matching key) so an input that matches
// more than one key returns ONE stable answer across many iterations.
//
// "Dallas, TX" is the cleanest case: under the buggy bare-substring pass it
// matches BOTH "dallas" (→ dallas) and "la" inside "dallas" (→ losangeles), so
// 400 iterations split ~218/182. Under the token-boundary fix only "dallas"
// matches, so all 400 must return "dallas".
//
// MUTATION-CHECK: restore the bare `strings.Contains` substring pass (dropping
// both token boundaries AND the longest-key-wins sort) → "Dallas, TX" matches
// "la" again → the 400-iteration run splits between dallas and losangeles →
// the single-stable-answer assertion fails → RED.
func TestResolveRegion_Determinism_StableAcrossIterations(t *testing.T) {
	const iters = 400
	loc := "Dallas, TX"
	want := craigslistRegions["dallas"]
	seen := map[string]int{}
	for i := 0; i < iters; i++ {
		region, ok := resolveRegion(loc)
		if !ok {
			t.Fatalf("iter %d: resolveRegion(%q) ok=false, want %q", i, loc, want)
		}
		seen[region]++
		if region != want {
			t.Errorf("iter %d: resolveRegion(%q) = %q, want %q (non-deterministic — map-order leak)", i, loc, region, want)
		}
	}
	if len(seen) != 1 {
		t.Errorf("resolveRegion(%q) returned %d distinct regions over %d iterations (%v) — must be a single stable answer", loc, len(seen), iters, seen)
	}
}

// --- F6: profile read is bounded ---
//
// The profile read runs on the raw caller context inside a concurrent source
// fan-out. With no sub-timeout, a saturated/unreachable pgxpool parks on pool
// acquisition until the caller context (the ~90s perSourceTimeout) cancels, so
// the config tier — the documented degradation — is never reached under DB
// stress. The fix wraps the profile read in its own short timeout
// (craigslistProfileTimeout); on timeout the profile tier returns "" and the
// config tier supplies the value.
//
// This test simulates a blocking pool: the profile seam parks until its context
// is cancelled, then returns "". With the sub-timeout (20ms) the profile tier
// unblocks at 20ms and the config tier is reached; without it (mutation) the
// seam parks on the 500ms caller context and the config tier is reached only at
// 500ms — well past the 200ms budget the assertion enforces.
//
// MUTATION-CHECK: in resolveCraigslistDefaultLocation replace
//
//	profileCtx, cancel := context.WithTimeout(ctx, craigslistProfileTimeout)
//	defer cancel()
//
// with
//
//	profileCtx := ctx
//
// (i.e. drop the sub-timeout wrap) → the seam parks on the 500ms caller ctx →
// elapsed ~500ms > 200ms budget → RED. The config value is still returned, but
// too late — the assertion is that the config tier is reached PROMPTLY, which
// is exactly what the sub-timeout guarantees and its absence breaks.
func TestResolveCraigslistDefaultLocation_ProfileReadBounded_ReachesConfigTier(t *testing.T) {
	saveDefaultLocationDeps(t)
	craigslistProfileTimeout = 20 * time.Millisecond
	// Use the map value (not a bare literal) so the test does not push the
	// "chicago" string over goconst's min-occurrences threshold.
	wantConfig := craigslistRegions["chicago"]
	engine.Cfg.CraigslistDefaultLocation = wantConfig

	// Simulate a saturated pgxpool: park until the context is cancelled, then
	// return "" (the DB query failed on the cancelled context).
	craigslistProfileLocation = func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "", nil
	}

	// Caller context simulates perSourceTimeout — generous (500ms) so the ONLY
	// thing that can bound the profile read is the sub-timeout, not the caller.
	callerCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	loc, tier := resolveCraigslistDefaultLocation(callerCtx)
	elapsed := time.Since(start)

	if loc != wantConfig || tier != "config" {
		t.Fatalf("resolveCraigslistDefaultLocation = (%q, %q), want (%q, %q) — config tier not reached", loc, tier, wantConfig, "config")
	}
	// The sub-timeout (20ms) must bound the profile read. Without it the seam
	// parks on the 500ms caller ctx. 200ms is a generous upper bound that the
	// fix (20ms) clears easily and the mutation (500ms) blows past.
	if elapsed > 200*time.Millisecond {
		t.Errorf("profile read was not bounded by the sub-timeout: elapsed %v > 200ms (config tier reached only after the caller ctx expired — sub-timeout wrap was removed)", elapsed)
	}
}

// --- F7: substitution is observable (log + counter) ---
//
// On a successful profile-tier substitution nothing previously recorded that a
// location was substituted or which tier supplied it — combined with #347 that
// is what made a wrong-city search undiagnosable. The fix logs at INFO and
// bumps craigslist_default_location_total{tier}. This test drives the REAL
// SearchCraigslistJobs wiring (not a direct IncrCraigslistDefaultLocation call,
// which would only prove the stdlib counter moves) and asserts the labelled
// counter incremented for the profile tier.
//
// MUTATION-CHECK: delete the `engine.IncrCraigslistDefaultLocation(tier)` line
// in SearchCraigslistJobs → the counter delta is 0 → RED.
func TestSearchCraigslistJobs_DefaultLocationSubstitution_IncrsCounter(t *testing.T) {
	saveDefaultLocationDeps(t)
	saveFetchVars(t)
	engine.Cfg.OxBrowserURL = "http://ox-browser-test:8901"
	engine.Cfg.CraigslistDefaultLocation = ""

	craigslistProfileLocation = func(_ context.Context) (string, error) { return "San Francisco Bay Area", nil }

	fixture, err := os.ReadFile("testdata/craigslist_html_jjj_warehouse.html")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	craigslistStealthFetch = stubStealthSuccess(fixture)
	craigslistOxFetchFetch = stubOxFetchSuccess(fixture)
	craigslistOxBrowserFetch = stubOx403

	key := engine.MetricCraigslistDefaultLocation + "{tier=profile}"
	before := engine.GetMetrics()[key]

	if _, err := SearchCraigslistJobs(context.Background(), "golang", "", 5); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	after := engine.GetMetrics()[key]
	if after <= before {
		t.Errorf("craigslist_default_location_total{tier=profile} did not increment (before=%d after=%d) — IncrCraigslistDefaultLocation call was deleted", before, after)
	}
}

// --- F8: city beats state (BLOCKING, round 3) ---
//
// The round-2 longest-wins tie-break made "Seattle, Washington" resolve to
// washingtondc 100% of the time: the tokenizer drops commas, so "washington"
// (10 chars) beat "seattle" (7 chars) — silently searching Washington DC for an
// operator in Washington state. The fix scopes the token pass to the FIRST
// comma-separated segment (the city in "City, State") and ranks by earliest
// match position, so the city wins and a state-only match in a later segment
// never fires. An unmapped city ("Spokane, Washington") is honestly unmapped
// (false), NOT the state — searching washingtondc for someone in Spokane is
// the same wrong-city outcome #347 exists to prevent.
//
// MUTATION-CHECK (F11): revert the state-tier rule → the six "City Washington"
// no-comma cases resolve to washingtondc → the not-washingtondc / wantOK=false
// assertions fail → RED.
// MUTATION-CHECK (F12): restore first-segment-only segmentation →
// "Remote, San Francisco, CA" → unmapped (city in segment 1 discarded) → RED.
// MUTATION-CHECK (F13): neuter the position comparison (posInSegment) →
// "Seattle Washington" → "washington" (longest, 10 > 7) wins → state-tier guard
// rejects (pos > 0) → false, but want seattle,true → RED. Without the guard too,
// → washingtondc → RED.
func TestResolveRegion_CityBeatsState_WashingtonCities(t *testing.T) {
	cases := []struct {
		loc    string
		want   string
		wantOK bool
	}{
		// City that IS a key — BOTH punctuation shapes must resolve to the
		// city, never to washingtondc.
		{"Seattle, Washington", craigslistRegions["seattle"], true},
		{"Seattle Washington", craigslistRegions["seattle"], true},
		{"Tacoma, Washington", craigslistRegions["tacoma"], true},
		{"Tacoma Washington", craigslistRegions["tacoma"], true},
		// City that is NOT a key — BOTH punctuation shapes must be unmapped,
		// NOT fall through to "washington" → washingtondc. The no-comma form
		// is the #347 regression this round closes: "Spokane Washington"
		// silently searched Washington DC.
		{"Spokane, Washington", "", false},
		{"Spokane Washington", "", false},
		{"Vancouver, Washington", "", false},
		{"Vancouver Washington", "", false},
		{"Bellevue, Washington", "", false},
		{"Bellevue Washington", "", false},
		{"Redmond, Washington", "", false},
		{"Redmond Washington", "", false},
		// Positive control: "WA" is not a key, so the state abbreviation does
		// not interfere and the city resolves on its own.
		{"Seattle, WA", craigslistRegions["seattle"], true},
		// Multi-token path: no comma, whole string is one segment; "san
		// francisco" matches at position 0 → sfbay.
		{"San Francisco Bay Area", craigslistCitySFBay, true},
		// Leading qualifier (LinkedIn-style "Remote, City, ST"): the city in
		// segment 1 must still resolve. Round 2 handled this; round 3's
		// first-segment-only segmentation regressed it — ranking across ALL
		// segments with a segment-index penalty restores it.
		{"Remote, San Francisco, CA", craigslistCitySFBay, true},
		{"Remote, Seattle, WA", craigslistRegions["seattle"], true},
		{"123 Main St, Seattle, WA", craigslistRegions["seattle"], true},
	}
	for _, c := range cases {
		got, ok := resolveRegion(c.loc)
		if ok && got == "washingtondc" {
			t.Errorf("resolveRegion(%q) = %q — state beat the city (round-2 regression): the city segment must win and an unmapped city must be unmapped, not the state", c.loc, got)
			continue
		}
		if ok != c.wantOK {
			t.Errorf("resolveRegion(%q): ok=%v, want %v (region=%q)", c.loc, ok, c.wantOK, got)
			continue
		}
		if ok && got != c.want {
			t.Errorf("resolveRegion(%q) = %q, want %q", c.loc, got, c.want)
		}
	}
}

// --- F9: cache invalidation (MAJOR, round 3) ---
//
// The profile-location cache is process-lifetime, but UpdateResumePerson (the
// admin UI POST /admin/resume/edit path) writes resume_persons.location
// IN-PROCESS with no restart. Without invalidation the connector keeps
// searching the old city until restart. The fix calls
// invalidateProfileLocationCache from UpdateResumePerson (and InsertPerson) so
// the next read re-queries.
//
// This test writes a new location through the SAME path the admin UI uses
// (db.UpdateResumePerson) and asserts the resolver returns the NEW value.
//
// MUTATION-CHECK: remove the invalidateProfileLocationCache() call from
// UpdateResumePerson → the cache keeps serving the pre-write value → the
// assertion "New York" fails (got "Seattle, WA") → RED.
//
// Requires DATABASE_URL pointing at a *_test Postgres; skips otherwise.
func TestProfileLocationCache_InvalidatedOnUpdateResumePerson(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	dbtest.RequireTestDB(t, dsn)

	ctx := context.Background()
	db, err := ConnectResumeDB(ctx, dsn)
	if err != nil {
		t.Fatalf("ConnectResumeDB: %v", err)
	}
	t.Cleanup(db.Close)

	// Drive the REAL loadCachedProfileLocation path: it reads
	// GetResumeDB().GetLatestPersonID, so register the test DB on the
	// package-global seam and restore it after.
	origDB := GetResumeDB()
	SetResumeDB(db)
	t.Cleanup(func() { SetResumeDB(origDB) })

	// Isolate the cache from other tests and reset it cold.
	saveDefaultLocationDeps(t)

	// Insert a person whose location is the pre-edit value. InsertPerson itself
	// invalidates the cache (the sibling hook), so the cache starts cold
	// regardless of prior state.
	personID, err := db.InsertPerson(ctx, PersonRecord{
		Name:     "Craigslist Cache Invalidation Test",
		Email:    "craigslist-cache-invalidation-test@example.com",
		Location: "Seattle, WA",
	})
	if err != nil {
		t.Fatalf("InsertPerson: %v", err)
	}
	t.Cleanup(func() { _ = db.ClearPerson(ctx, personID) })

	// First read populates the cache with the pre-edit location.
	first, err := loadCachedProfileLocation(ctx)
	if err != nil {
		t.Fatalf("first loadCachedProfileLocation: %v", err)
	}
	if first != "Seattle, WA" {
		t.Fatalf("first read = %q, want %q (setup: cache must seed with the pre-edit value)", first, "Seattle, WA")
	}

	// Update the location through the SAME path the admin UI uses.
	if err := db.UpdateResumePerson(ctx, personID, PersonRecord{
		ID:       personID,
		Name:     "Craigslist Cache Invalidation Test",
		Email:    "craigslist-cache-invalidation-test@example.com",
		Location: "New York",
	}); err != nil {
		t.Fatalf("UpdateResumePerson: %v", err)
	}

	// Second read must reflect the NEW value — the cache was invalidated.
	second, err := loadCachedProfileLocation(ctx)
	if err != nil {
		t.Fatalf("second loadCachedProfileLocation: %v", err)
	}
	if second != "New York" {
		t.Errorf("cache not invalidated after UpdateResumePerson: got %q, want %q — the connector would keep searching the old city until restart", second, "New York")
	}
}

// --- F10: profile read failure is distinguishable from no profile (MINOR 3) ---
//
// A profile read that errors or times out must NOT collapse into the same
// "config" tier as a deployment with no profile location — a chronically
// saturated pool would otherwise look exactly like a no-profile deployment and
// the 2s-timeout degradation would have no distinct signal. The fix returns a
// distinct tier "config_after_profile_error" so the
// craigslist_default_location_total{tier} counter separates the two.
//
// MUTATION-CHECK: collapse the error branch back to "config" (or revert
// loadCachedProfileLocation to return "" with no error) → tier=="config" → the
// assertion tier=="config_after_profile_error" fails → RED.
func TestResolveCraigslistDefaultLocation_ProfileError_DistinctTier(t *testing.T) {
	saveDefaultLocationDeps(t)
	wantConfig := craigslistRegions["chicago"]
	engine.Cfg.CraigslistDefaultLocation = wantConfig

	craigslistProfileLocation = func(_ context.Context) (string, error) {
		return "", errors.New("pgxpool: connection pool exhausted")
	}

	loc, tier := resolveCraigslistDefaultLocation(context.Background())
	if loc != wantConfig {
		t.Fatalf("loc = %q, want %q", loc, wantConfig)
	}
	if tier != "config_after_profile_error" {
		t.Errorf("tier = %q, want %q — a profile read failure must be distinguishable from no profile", tier, "config_after_profile_error")
	}

	// The tier label must be accepted by the counter (not silently dropped by
	// the cardinality guard) — assert it increments.
	key := engine.MetricCraigslistDefaultLocation + "{tier=config_after_profile_error}"
	before := engine.GetMetrics()[key]
	engine.IncrCraigslistDefaultLocation(tier)
	after := engine.GetMetrics()[key]
	if after <= before {
		t.Errorf("craigslist_default_location_total{tier=config_after_profile_error} did not increment (before=%d after=%d) — tier label rejected by the cardinality guard", before, after)
	}
}

// --- F14: all three tiers render on the flat /metrics endpoint (MAJOR) ---
//
// FormatMetrics pre-touches craigslist_default_location_total{tier} so a
// rate()-floor alert sees 0 before the first substituted-location search.
// warmAlertBoundedMetrics ranges over validCraigslistDefaultLocationTiers and
// picks up "config_after_profile_error"; FormatMetrics had a hardcoded
// []string{"profile","config"} and rendered only its own allowlist, so the
// tier that exists specifically to make a saturated pool visible was ABSENT
// on /metrics. The fix makes FormatMetrics range over the same map.
//
// MUTATION-CHECK: revert FormatMetrics to the hardcoded pair → the
// config_after_profile_error assertion fails → RED.
func TestFormatMetrics_CraigslistDefaultLocationAllTiers(t *testing.T) {
	out := engine.FormatMetrics()
	for _, tier := range []string{"profile", "config", "config_after_profile_error"} {
		label := engine.MetricCraigslistDefaultLocation + "{tier=" + tier + "}"
		if !strings.Contains(out, label) {
			t.Errorf("FormatMetrics output must contain %q; got:\n%s", label, out)
		}
	}
}

// --- Item 5b: profile error + no config is not silent (MAJOR) ---
//
// When the profile read errors AND CraigslistDefaultLocation is empty,
// resolveCraigslistDefaultLocation returns ("", "") — the caller skips the
// counter and the INFO log, and perr is discarded. A saturated pool on a
// no-config deployment is silent: the same blind spot the
// config_after_profile_error tier was added to remove, one branch over. The
// fix logs the profile error at WARN when there is no config fallback to
// substitute, so the degradation is observable.
//
// MUTATION-CHECK: remove the WARN log in the no-config-error path → the
// "level=WARN" / "profile read failed" assertions fail → RED.
func TestResolveCraigslistDefaultLocation_ProfileErrorNoConfig_Logs(t *testing.T) {
	saveDefaultLocationDeps(t)
	engine.Cfg.CraigslistDefaultLocation = ""

	craigslistProfileLocation = func(_ context.Context) (string, error) {
		return "", errors.New("pgxpool: connection pool exhausted")
	}

	var buf bytes.Buffer
	orig := slog.Default()
	t.Cleanup(func() { slog.SetDefault(orig) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))

	loc, tier := resolveCraigslistDefaultLocation(context.Background())
	if loc != "" || tier != "" {
		t.Fatalf("resolveCraigslistDefaultLocation = (%q, %q), want (\"\", \"\") — no config fallback must not substitute", loc, tier)
	}

	out := buf.String()
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("profile read error with no config fallback must log at WARN; got:\n%s", out)
	}
	if !strings.Contains(out, "profile read failed") {
		t.Errorf("WARN log must carry the profile read error; got:\n%s", out)
	}
}

// --- Item 5a: ClearPerson / ClearAllPersons invalidate the profile cache ---
//
// ClearPerson and ClearAllPersons change what GetLatestPersonID returns but
// neither invalidated the memo. Covered today only because master_resume.go
// follows ClearAllPersons with InsertPerson (which does invalidate) — so if
// that insert errors, the cache serves a deleted person's location until the
// next successful rebuild. The fix calls invalidateProfileLocationCache from
// both clear paths.
//
// MUTATION-CHECK: remove the invalidateProfileLocationCache() call from
// ClearPerson → the second read returns the stale cached value → RED.
//
// Requires DATABASE_URL pointing at a *_test Postgres; skips otherwise.
func TestProfileLocationCache_InvalidatedOnClearPerson(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	dbtest.RequireTestDB(t, dsn)

	ctx := context.Background()
	db, err := ConnectResumeDB(ctx, dsn)
	if err != nil {
		t.Fatalf("ConnectResumeDB: %v", err)
	}
	t.Cleanup(db.Close)

	origDB := GetResumeDB()
	SetResumeDB(db)
	t.Cleanup(func() { SetResumeDB(origDB) })

	saveDefaultLocationDeps(t)

	personID, err := db.InsertPerson(ctx, PersonRecord{
		Name:     "Craigslist Clear Cache Test",
		Email:    "craigslist-clear-cache-test@example.com",
		Location: "Seattle, WA",
	})
	if err != nil {
		t.Fatalf("InsertPerson: %v", err)
	}

	// First read populates the cache.
	first, err := loadCachedProfileLocation(ctx)
	if err != nil {
		t.Fatalf("first loadCachedProfileLocation: %v", err)
	}
	if first != "Seattle, WA" {
		t.Fatalf("first read = %q, want %q", first, "Seattle, WA")
	}

	// Clear the person — this changes what GetLatestPersonID returns.
	if err := db.ClearPerson(ctx, personID); err != nil {
		t.Fatalf("ClearPerson: %v", err)
	}

	// Second read must NOT serve the stale cached value — the cache was
	// invalidated, so it re-queries and finds no person (GetLatestPersonID=0).
	second, err := loadCachedProfileLocation(ctx)
	if err != nil {
		t.Fatalf("second loadCachedProfileLocation: %v", err)
	}
	if second != "" {
		t.Errorf("cache not invalidated after ClearPerson: got %q, want \"\" — the connector would keep searching a deleted person's city", second)
	}
}
