package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
