package jobs

// ats_lever_test.go guards the lever secondary-discovery fallback (P4 lever fix).
//
// Revert-red: removing the secondary discovery block (the "if len(slugs) == 0"
// block that tries leverSiteSearch + query) causes TestLever_SecondaryDiscovery
// to fail: the test configures the discoverer to return lever URLs only on the
// second call (site-scope-first query), so the primary call returns nothing and
// slugs stays empty — SearchLeverJobs returns nil instead of results.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// countingDiscoverer records each query string and returns a configurable result.
type countingDiscoverer struct {
	calls   atomic.Int64
	queries []string
	// respondOnCall: which 1-based call number to return results for (0 = all calls)
	respondOnCall int
	results       []engine.SearxngResult
}

func (d *countingDiscoverer) DiscoverBoardURLs(_ context.Context, query string) ([]engine.SearxngResult, error) {
	n := int(d.calls.Add(1))
	d.queries = append(d.queries, query)
	if d.respondOnCall == 0 || n == d.respondOnCall {
		return d.results, nil
	}
	return nil, nil
}

// TestLever_SecondaryDiscovery — the load-bearing regression guard.
// When the primary discovery query ("golang site:jobs.lever.co") returns no
// lever URLs, SearchLeverJobs must try a secondary query
// ("site:jobs.lever.co golang") and use its results.
func TestLever_SecondaryDiscovery(t *testing.T) {
	resetATSDiscoverer(t)
	// Ensure the lever circuit breaker is closed before the test. Other tests in
	// the package may have driven it open via Record(false) calls on failed HTTP
	// requests. ForceHalfOpen + Record(true) closes it without a 30s cooldown.
	leverBreaker.ForceHalfOpen()
	leverBreaker.Record(true)

	// Set up a lever API stub for the slug returned by secondary discovery.
	const slug = "testco"
	leverStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/"+slug) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// "golang" in text satisfies the keyword filter in SearchLeverJobs.
		_, _ = w.Write([]byte(`[{"id":"abc123","text":"Golang Engineer","hostedUrl":"https://jobs.lever.co/` + slug + `/abc123","categories":{"location":"Remote","team":"Engineering","commitment":"Full-time"}}]`))
	}))
	t.Cleanup(leverStub.Close)

	// Discoverer returns a lever URL only on the SECOND call (secondary query).
	d := &countingDiscoverer{
		respondOnCall: 2,
		results: []engine.SearxngResult{
			{URL: "https://jobs.lever.co/" + slug + "/abc123", Title: slug},
		},
	}
	ATSDiscoverer = d

	// Redirect the lever API calls to our stub.
	origClient := engine.Cfg.HTTPClient
	engine.Cfg.HTTPClient = &http.Client{
		Transport: &redirectTransport{target: leverStub.URL},
	}
	t.Cleanup(func() { engine.Cfg.HTTPClient = origClient })

	results, err := SearchLeverJobs(context.Background(), "golang", "", 5)
	if err != nil {
		t.Fatalf("SearchLeverJobs: unexpected error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("SearchLeverJobs returned 0 results — secondary discovery fallback did not fire; " +
			"removing the secondary discovery block would cause this failure")
	}

	calls := int(d.calls.Load())
	if calls < 2 {
		t.Errorf("discoverer called %d times; want ≥2 (primary + secondary)", calls)
	}

	// Verify the secondary query starts with the site scope (the key difference).
	var foundScopedFirst bool
	for _, q := range d.queries {
		if strings.HasPrefix(q, leverSiteSearch) {
			foundScopedFirst = true
		}
	}
	if !foundScopedFirst {
		t.Errorf("no query started with %q; secondary query must lead with site scope; queries: %v",
			leverSiteSearch, d.queries)
	}
}

// TestExtractLeverSlugs_FromURLs verifies slug extraction from jobs.lever.co URLs.
func TestExtractLeverSlugs_FromURLs(t *testing.T) {
	results := []engine.SearxngResult{
		{URL: "https://jobs.lever.co/stripe/engineer-123", Title: "Stripe"},
		{URL: "https://jobs.lever.co/airbnb/designer-456", Title: "Airbnb"},
		{URL: "https://jobs.lever.co/stripe/other-789", Title: "Stripe 2"},    // dup slug
		{URL: "https://boards.greenhouse.io/not-lever", Title: "Greenhouse"},   // wrong domain
		{URL: "https://jobs.lever.co/", Title: "Root (no slug)"},              // no slug
	}
	slugs := extractLeverSlugs(results)

	want := []string{"stripe", "airbnb"}
	if len(slugs) != len(want) {
		t.Fatalf("extractLeverSlugs: got %v, want %v", slugs, want)
	}
	for i, s := range slugs {
		if s != want[i] {
			t.Errorf("[%d] slug = %q, want %q", i, s, want[i])
		}
	}
}
