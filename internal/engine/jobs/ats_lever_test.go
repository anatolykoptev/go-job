package jobs

// ats_lever_test.go guards the lever multi-variant discovery (P1 union).
//
// Revert-red: collapsing unionDiscoverSlugs to a single variant causes
// TestLever_SecondaryDiscovery to fail: the test configures the discoverer to
// return lever URLs only when the query leads with the site scope
// ("site:jobs.lever.co golang"), so a single-variant (primary-only) path
// returns nothing and SearchLeverJobs returns nil instead of results.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// countingDiscoverer records each query string (thread-safe) and returns a
// configurable result. respondOnScopedFirst controls whether it returns results
// only for queries that begin with the site-scope prefix (V2 variant test mode).
type countingDiscoverer struct {
	calls atomic.Int64
	mu    sync.Mutex
	queries []string
	// respondOnScopedFirst: when true, return results only when the query
	// starts with leverSiteSearch (the V2 "scope-first" variant).
	// When false, respond to all queries.
	respondOnScopedFirst bool
	results              []engine.SearxngResult
}

func (d *countingDiscoverer) DiscoverBoardURLs(_ context.Context, query string) ([]engine.SearxngResult, error) {
	d.calls.Add(1)
	d.mu.Lock()
	d.queries = append(d.queries, query)
	d.mu.Unlock()
	if d.respondOnScopedFirst && !strings.HasPrefix(query, leverSiteSearch) {
		return nil, nil
	}
	return d.results, nil
}

// TestLever_SecondaryDiscovery — the load-bearing regression guard.
// When the primary discovery query ("golang site:jobs.lever.co") returns no
// lever URLs, SearchLeverJobs must still produce results via the scope-first
// variant ("site:jobs.lever.co golang") — which is variant V2 in the union.
//
// With unionDiscoverSlugs all variants run in parallel; this test ensures the
// V2 (scope-first) variant is included and its results are collected.
// Revert-red: removing V2 from discoveryVariants for lever would remove the
// scope-first query and this test's discoverer would return nothing → 0 results.
func TestLever_SecondaryDiscovery(t *testing.T) {
	resetATSDiscoverer(t)
	resetSlugCache(t)
	SetSlugCache(nil)
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

	// Discoverer returns lever URL only when the query starts with the site scope
	// (the V2 / scope-first variant). Primary variant ("golang site:jobs.lever.co")
	// returns nothing — testing that the union still surfaces results from V2.
	d := &countingDiscoverer{
		respondOnScopedFirst: true,
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
		t.Fatal("SearchLeverJobs returned 0 results — scope-first variant (V2) did not fire; " +
			"removing V2 from discoveryVariants for lever would cause this failure")
	}

	calls := int(d.calls.Load())
	if calls < 2 {
		t.Errorf("discoverer called %d times; want ≥2 (multiple variants)", calls)
	}

	// Verify the V2 (scope-first) query was issued.
	d.mu.Lock()
	queries := make([]string, len(d.queries))
	copy(queries, d.queries)
	d.mu.Unlock()

	var foundScopedFirst bool
	for _, q := range queries {
		if strings.HasPrefix(q, leverSiteSearch) {
			foundScopedFirst = true
		}
	}
	if !foundScopedFirst {
		t.Errorf("no query started with %q; V2 (scope-first) variant must be present; queries: %v",
			leverSiteSearch, queries)
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
