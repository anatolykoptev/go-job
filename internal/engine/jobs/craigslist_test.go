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
	origOxRead := craigslistOxBrowserReadFetch
	t.Cleanup(func() {
		craigslistStealthFetch = origStealth
		craigslistOxBrowserFetch = origOx
		craigslistOxBrowserReadFetch = origOxRead
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

// --- HTML parser tests ---

// TestParseCraigslistHTMLStatic_RealFixture validates the tier-1 parser against
// a REAL captured Craigslist no-JS search page (cat=jjj, query=warehouse,
// sfbay). The fixture has 20 li.cl-static-search-result rows with title, URL
// and location. Captured live via curl from www.craigslist.org — the front door
// that stands open while the RSS door is locked.
func TestParseCraigslistHTMLStatic_RealFixture(t *testing.T) {
	body, err := os.ReadFile("testdata/craigslist_html_static_jjj.html")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	results, err := parseCraigslistHTMLStatic(body, 100)
	if err != nil {
		t.Fatalf("parseCraigslistHTMLStatic failed on real fixture: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("parseCraigslistHTMLStatic returned 0 results from a real 20-item fixture")
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
	// Location must be populated and entity-decoded.
	if first.Metadata["location"] == "" {
		t.Errorf("first result missing location metadata: %+v", first.Metadata)
	}
	// Content must carry the location.
	if !strings.Contains(first.Content, "Location:") {
		t.Errorf("first result content missing location: %s", first.Content)
	}
	// Entity decoding: the fixture has titles with &amp; — html.Parse decodes
	// entities in text nodes and attributes, so the title must NOT contain
	// literal "&amp;".
	for i, r := range results {
		if strings.Contains(r.Title, "&amp;") {
			t.Errorf("result %d title has undecoded &amp;: %s", i, r.Title)
		}
		if strings.Contains(r.Title, "&#39;") {
			t.Errorf("result %d title has undecoded &#39;: %s", i, r.Title)
		}
	}
	t.Logf("parsed %d results from real static fixture; first: title=%q url=%q loc=%q",
		len(results), first.Title, first.URL, first.Metadata["location"])
}

// TestParseCraigslistHTMLRendered_RealFixture validates the tier-2 parser
// against a REAL captured ox-browser-rendered Craigslist search page
// (cat=jjj, query=warehouse forklift operator, sfbay). The fixture has 12
// div.cl-search-result rows with posting-title, href and result-location.
// Captured live via go-wowa chrome_interact evaluate on the rendered DOM.
func TestParseCraigslistHTMLRendered_RealFixture(t *testing.T) {
	body, err := os.ReadFile("testdata/craigslist_html_rendered_jjj.html")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	results, err := parseCraigslistHTMLRendered(body, 100)
	if err != nil {
		t.Fatalf("parseCraigslistHTMLRendered failed on real fixture: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("parseCraigslistHTMLRendered returned 0 results from a real 12-item fixture")
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
	t.Logf("parsed %d results from real rendered fixture; first: title=%q url=%q loc=%q",
		len(results), first.Title, first.URL, first.Metadata["location"])
}

// TestParseCraigslistHTML_WrongFixture_ReturnsErrParse is test 1b: a tier whose
// fetch succeeds but whose selector matches nothing returns ErrParse, NOT an
// empty result. Swapping the two fixtures between the two parsers must produce
// errors, not silent zeros — the two tiers serve DIFFERENT markup and using one
// selector for both yields a silent zero from the other.
func TestParseCraigslistHTML_WrongFixture_ReturnsErrParse(t *testing.T) {
	staticBody, err := os.ReadFile("testdata/craigslist_html_static_jjj.html")
	if err != nil {
		t.Fatalf("read static fixture: %v", err)
	}
	renderedBody, err := os.ReadFile("testdata/craigslist_html_rendered_jjj.html")
	if err != nil {
		t.Fatalf("read rendered fixture: %v", err)
	}

	// Static parser on rendered markup: 0 li.cl-static-search-result → ErrParse.
	if _, err := parseCraigslistHTMLStatic(renderedBody, 100); err == nil {
		t.Fatal("parseCraigslistHTMLStatic on rendered markup returned nil error — must be ErrParse (selector mismatch is not silent empty)")
	} else if !errors.Is(err, ErrParse) {
		t.Errorf("parseCraigslistHTMLStatic on rendered markup: error does not wrap ErrParse: %v", err)
	}

	// Rendered parser on static markup: 0 div.cl-search-result → ErrParse.
	if _, err := parseCraigslistHTMLRendered(staticBody, 100); err == nil {
		t.Fatal("parseCraigslistHTMLRendered on static markup returned nil error — must be ErrParse (selector mismatch is not silent empty)")
	} else if !errors.Is(err, ErrParse) {
		t.Errorf("parseCraigslistHTMLRendered on static markup: error does not wrap ErrParse: %v", err)
	}
}

// TestParseCraigslistHTML_EntityDecoding is test 5: a title containing &amp;
// and &#x0024; must be decoded in the returned result. html.Parse decodes
// entities in text nodes and attributes automatically.
func TestParseCraigslistHTML_EntityDecoding(t *testing.T) {
	// Synthetic static-markup li with both &amp; and &#x0024; in the title.
	body := []byte(`<ol class="cl-static-search-results">
<li class="cl-static-search-result" title="Warehouse &amp; Delivery &#x0024;25/hr">
<a href="https://www.craigslist.org/view/d/test-warehouse/abc123"><div class="title">Warehouse &amp; Delivery &#x0024;25/hr</div><div class="details"><div class="price">$0</div><div class="location">downtown</div></div></a>
</li>
</ol>`)

	results, err := parseCraigslistHTMLStatic(body, 100)
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
	body, err := os.ReadFile("testdata/craigslist_html_static_jjj.html")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	results, err := parseCraigslistHTMLStatic(body, 5)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(results) > 5 {
		t.Errorf("expected at most 5 results, got %d", len(results))
	}
}

// --- RSS parser tests (updated to assert decoded content — 3b) ---

// TestParseCraigslistRSS_RealFixture validates the RSS parser against a REAL
// captured Craigslist RSS feed (RDF/XML, RSS 1.0 format). The fixture is from
// anaisbetts/cl-apartment-finder — a real sfbay.craigslist.org RSS capture (23 items).
//
// 3b: the fixture's first title contains encoded entities (&#x0024; for $, raw
// & in CDATA). The parser must decode them — assert on decoded content, not
// just Title != "".
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
	// 3b: entity decoding — the fixture title has &#x0024; (hex $) which must
	// decode to "$", and must NOT contain the raw entity.
	if strings.Contains(first.Title, "&#x0024;") {
		t.Errorf("first result title has undecoded &#x0024;: %s", first.Title)
	}
	if !strings.Contains(first.Title, "$") {
		t.Errorf("first result title missing decoded $ sign: %s", first.Title)
	}
	// 3b: description must have HTML tags stripped (CDATA carries raw HTML).
	if strings.Contains(first.Content, "<br") || strings.Contains(first.Content, "<p>") {
		t.Errorf("first result content has unstripped HTML tags: %s", first.Content)
	}
	t.Logf("parsed %d results; first title=%q", len(results), first.Title)
}

// TestParseCraigslistRSS_EntityDecoding is test 5 for the RSS path: a title
// with &amp; and &#x0024; in CDATA must be decoded.
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
	// Description: tags stripped + entity decoded.
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

// --- Ladder / SearchCraigslistJobs tests ---

// TestSearchCraigslistJobs_Stealth403OxBrowserSuccess verifies the two-tier
// escalation: stealth returns 403, ox-browser /read returns the rendered page →
// results returned. This is the observable escalation: if the fallback didn't
// fire, we'd get a blocked error instead of results.
func TestSearchCraigslistJobs_Stealth403OxBrowserReadSuccess(t *testing.T) {
	saveFetchVars(t)

	fixture, err := os.ReadFile("testdata/craigslist_html_rendered_jjj.html")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	craigslistStealthFetch = stubStealth403
	craigslistOxBrowserReadFetch = stubOxSuccess(fixture)
	craigslistOxBrowserFetch = stubOx403 // RSS tier-2 shouldn't be needed

	results, err := SearchCraigslistJobs(context.Background(), "warehouse", "sfbay", 100)
	if err != nil {
		t.Fatalf("expected results, got error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results from ox-browser /read fallback, got 0 — escalation did not fire")
	}
}

// TestSearchCraigslistJobs_AllTiers403_ReturnsBlockedError is test 2: all tiers
// refused (403) → blocked error, NOT (nil, nil). This is the EXACT regression
// that shipped: both tiers refused and the connector returned (nil, nil) —
// reporting success with zero rows.
func TestSearchCraigslistJobs_AllTiers403_ReturnsBlockedError(t *testing.T) {
	saveFetchVars(t)

	craigslistStealthFetch = stubStealth403
	craigslistOxBrowserReadFetch = stubOx403
	craigslistOxBrowserFetch = stubOx403

	results, err := SearchCraigslistJobs(context.Background(), "warehouse", "sfbay", 100)
	if err == nil {
		t.Fatal("expected blocked error when all tiers return 403, got nil — this is the shipped defect (nil, nil → outcome=empty, error=0)")
	}
	if results != nil {
		t.Errorf("expected nil results on blocked, got %d results", len(results))
	}
	if !errors.Is(err, errCraigslistBlocked) {
		t.Errorf("expected errCraigslistBlocked, got: %v", err)
	}
}

// TestSearchCraigslistJobs_Tier1ChallengeEscalates is test 3 (3e): HTTP 200
// with a challenge/HTML body on tier 1 → escalates rather than aborting. A
// 200 carrying a challenge page has no cl-static-search-result elements, so the
// parser returns ErrParse; the ladder must treat this as a soft-block signal
// and escalate to tier 2, NOT return the parse error immediately.
func TestSearchCraigslistJobs_Tier1ChallengeEscalates(t *testing.T) {
	saveFetchVars(t)

	challenge := []byte(`<!DOCTYPE html><html><head><title>Are you human?</title></head><body><div class="challenge">verify you are not a bot</div></body></html>`)
	fixture, err := os.ReadFile("testdata/craigslist_html_rendered_jjj.html")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	craigslistStealthFetch = stubStealthSuccess(challenge)
	craigslistOxBrowserReadFetch = stubOxSuccess(fixture)
	craigslistOxBrowserFetch = stubOx403

	results, err := SearchCraigslistJobs(context.Background(), "warehouse", "sfbay", 100)
	if err != nil {
		t.Fatalf("expected escalation to tier 2 to yield results, got error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("tier 1 challenge did not escalate to tier 2 — got 0 results (3e: 200 but wrong format must escalate, not abort)")
	}
}

// TestSearchCraigslistJobs_AllTiersTransportError_NotBlocked is test 4 (3a):
// transport error (not a refusal) on every tier → the returned error carries
// the underlying cause, and errors.Is(err, errCraigslistBlocked) is FALSE.
// This prevents an ox-browser outage from reading to an operator as "Craigslist
// blocked our IP" and keeps context.DeadlineExceeded reaching PlatformOutcome
// as outcome=timeout, not downgraded to outcome=error.
func TestSearchCraigslistJobs_AllTiersTransportError_NotBlocked(t *testing.T) {
	saveFetchVars(t)

	stealthErr := errors.New("stealth: dial timeout")
	craigslistStealthFetch = func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		return 0, nil, stealthErr
	}
	craigslistOxBrowserReadFetch = func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		return 0, nil, errors.New("ox-browser /read: connection refused")
	}
	craigslistOxBrowserFetch = stubOxError

	_, err := SearchCraigslistJobs(context.Background(), "warehouse", "sfbay", 100)
	if err == nil {
		t.Fatal("expected error when all tiers fail with transport errors, got nil")
	}
	if errors.Is(err, errCraigslistBlocked) {
		t.Errorf("transport errors must NOT be classified as blocked (3a): %v", err)
	}
	// The underlying cause must be preserved.
	if !strings.Contains(err.Error(), "dial timeout") && !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("returned error does not carry the underlying cause: %v", err)
	}
}

// TestSearchCraigslistJobs_ValidEmptyRSS_ReturnsNil is test 6: a valid RSS feed
// with zero items returns (nil, nil) — genuine empty stays genuine empty.
// The HTML tiers get the RSS XML (wrong format → ErrParse → escalate), and the
// RSS tier parses it as a valid 0-item feed → nil, nil.
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
	craigslistOxBrowserReadFetch = stubOx403 // HTML tier-2 refused → escalate to RSS
	craigslistOxBrowserFetch = stubOx403     // RSS tier-2 shouldn't be called (tier-1 succeeds)

	results, err := SearchCraigslistJobs(context.Background(), "warehouse", "sfbay", 100)
	if err != nil {
		t.Fatalf("expected nil error for genuine empty feed, got: %v", err)
	}
	if results != nil {
		t.Errorf("expected nil results for genuine empty feed, got %d results", len(results))
	}
}

// TestSearchCraigslistJobs_StealthSuccess_NoEscalation verifies that when
// stealth HTML tier-1 succeeds, the ox-browser tiers are never called.
func TestSearchCraigslistJobs_StealthSuccess_NoEscalation(t *testing.T) {
	saveFetchVars(t)

	fixture, err := os.ReadFile("testdata/craigslist_html_static_jjj.html")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	oxReadCalled := false
	oxCalled := false
	craigslistStealthFetch = stubStealthSuccess(fixture)
	craigslistOxBrowserReadFetch = func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		oxReadCalled = true
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
	if oxReadCalled {
		t.Error("ox-browser /read tier was called even though stealth succeeded — should short-circuit")
	}
	if oxCalled {
		t.Error("ox-browser /fetch-smart tier was called even though stealth succeeded — should short-circuit")
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

// Ensure engine.Config is initialized for tests that reference engine.Cfg fields.
var _ = engine.Config{}
var _ stealth.BrowserClient
