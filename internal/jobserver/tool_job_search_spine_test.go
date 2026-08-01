package jobserver

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/anatolykoptev/go-kit/llm"
	"github.com/anatolykoptev/go_job/internal/engine"
)

// These tests falsify the deterministic-listing-spine change: the returned
// list is a deterministic UNION ordered by relevance (structured listings are
// the spine, the LLM a contributor), and an LLM failure no longer discards
// structured work. Each test drives the REAL runJobSearch path via the
// summarizeJobResults seam + withTestRegistry; none calls a helper directly.
//
// No t.Parallel(): every test swaps the package-level summarizeJobResults var.

// stubSummarize swaps the package-level summarizeJobResults seam and restores
// it on cleanup. Returns the original for manual restoration if needed.
func stubSummarize(t *testing.T, fn func(_ context.Context, query, _ string, _ int, _ []engine.SearxngResult, _ map[string]string) (*engine.JobSearchOutput, error)) {
	t.Helper()
	orig := summarizeJobResults
	t.Cleanup(func() { summarizeJobResults = orig })
	summarizeJobResults = fn
}

// F1 — LLM returns an error -> output still contains the structured listings,
// ranked. The structured listings are the spine; an LLM 529 must not discard
// them.
//
// Mutation: tool_job_search.go, in the `if err != nil` block, replace
// `jobOut = &engine.JobSearchOutput{Query: input.Query}` with an early
// `return nil, engine.JobSearchOutput{Query: input.Query, Summary: "LLM failed", Sources: sources}, nil`
// (restoring the old discard-everything behaviour) -> finalJobs never built ->
// out.Jobs nil -> RED.
func TestSpine_F1_LLMErrorServesStructured(t *testing.T) {
	const urlA = "https://jobs.lever.co/testco/aaa"
	const urlB = "https://jobs.greenhouse.io/testco/bbb"
	src := testStructuredSource{
		results: []engine.SearxngResult{
			{URL: urlA, Title: "Go Backend", Content: "** Go Backend at TestCo"},
			{URL: urlB, Title: "Distributed Sys", Content: "** Distributed Sys at TestCo"},
		},
		listings: []engine.JobListing{
			{URL: urlA, Title: "Go Backend", Company: "TestCo", Source: "lever"},
			{URL: urlB, Title: "Distributed Sys", Company: "TestCo", Source: "greenhouse"},
		},
	}
	withTestRegistry(t, src)

	stubSummarize(t, func(_ context.Context, _, _ string, _ int, _ []engine.SearxngResult, _ map[string]string) (*engine.JobSearchOutput, error) {
		return nil, &llm.APIError{StatusCode: 529, Body: "Overloaded"}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	input := engine.JobSearchInput{Query: "f1-llm-error-unique", Platform: "all"}

	_, out, err := runJobSearch(ctx, nil, input)
	if err != nil {
		t.Fatalf("runJobSearch returned error: %v", err)
	}
	if len(out.Jobs) != 2 {
		t.Fatalf("F1 FAIL: len(out.Jobs) = %d, want 2 (LLM error must not discard structured listings)", len(out.Jobs))
	}
	// Both structured listings must survive.
	urls := map[string]bool{out.Jobs[0].URL: true, out.Jobs[1].URL: true}
	if !urls[urlA] || !urls[urlB] {
		t.Fatalf("F1 FAIL: expected both structured URLs; got %v", urls)
	}
	// Summary must carry the error CLASS, not the raw error text, and state
	// the listings are complete.
	if !contains(out.Summary, "unavailable") {
		t.Errorf("F1: summary must state prose unavailable; got: %s", out.Summary)
	}
	if !contains(out.Summary, "overloaded") {
		t.Errorf("F1: summary must carry the LLM error class 'overloaded'; got: %s", out.Summary)
	}
	if contains(out.Summary, "529") {
		t.Errorf("F1: summary must NOT carry raw error text (529); got: %s", out.Summary)
	}
}

// F2 — LLM returns listings the structured set does not have -> those survive
// (the union is a union, not a replacement).
//
// Mutation: tool_job_search.go, in the LLM-only append loop, replace the loop
// body with `continue` (skip ALL LLM jobs) -> URL B (LLM-only) dropped -> RED.
func TestSpine_F2_LLMOnlyListingsSurvive(t *testing.T) {
	const urlA = "https://jobs.lever.co/testco/aaa" // structured-backed
	const urlB = "https://news.ycombinator.com/item?id=999" // LLM-only (no structured)
	src := testStructuredSource{
		results:  []engine.SearxngResult{{URL: urlA, Title: "Go Backend", Content: "** Go Backend at TestCo"}},
		listings: []engine.JobListing{{URL: urlA, Title: "Go Backend", Company: "TestCo", Source: "lever"}},
	}
	withTestRegistry(t, src)

	stubSummarize(t, func(_ context.Context, query, _ string, _ int, _ []engine.SearxngResult, _ map[string]string) (*engine.JobSearchOutput, error) {
		return &engine.JobSearchOutput{
			Query:   query,
			Summary: "2 results",
			Jobs: []engine.JobListing{
				{Title: "Go Backend", Company: "TestCo", URL: urlA},
				{Title: "HN Text Post", Company: "Startup", URL: urlB},
			},
		}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	input := engine.JobSearchInput{Query: "f2-llm-only-unique", Platform: "all"}

	_, out, err := runJobSearch(ctx, nil, input)
	if err != nil {
		t.Fatalf("runJobSearch returned error: %v", err)
	}
	if len(out.Jobs) != 2 {
		t.Fatalf("F2 FAIL: len(out.Jobs) = %d, want 2 (union must keep LLM-only listings)", len(out.Jobs))
	}
	urls := map[string]bool{}
	for _, j := range out.Jobs {
		urls[j.URL] = true
	}
	if !urls[urlB] {
		t.Errorf("F2 FAIL: LLM-only URL %q must survive in the union; got URLs: %v", urlB, urls)
	}
}

// F3 — A field present in the structured listing and also present in the LLM
// listing -> the structured value wins. A field EMPTY in the structured listing
// and present in the LLM listing -> the LLM value survives.
//
// Mutation: tool_job_search.go, delete the `jobs.FillStructuredFromLLM(&s, llm)`
// call in the structured-backed loop -> Company stays "" (LLM not filled) -> RED.
func TestSpine_F3_StructuredWins_LLMFillsEmpty(t *testing.T) {
	const urlA = "https://jobs.lever.co/testco/aaa"
	src := testStructuredSource{
		results:  []engine.SearxngResult{{URL: urlA, Title: "Go Backend", Content: "** Go Backend at TestCo"}},
		listings: []engine.JobListing{{URL: urlA, Title: "Structured Title", Source: "lever"}}, // Company intentionally empty
	}
	withTestRegistry(t, src)

	stubSummarize(t, func(_ context.Context, query, _ string, _ int, _ []engine.SearxngResult, _ map[string]string) (*engine.JobSearchOutput, error) {
		return &engine.JobSearchOutput{
			Query:   query,
			Summary: "1 result",
			Jobs: []engine.JobListing{{
				Title:   "LLM Title", // must NOT overwrite structured
				Company: "LLM Co",    // must fill the empty structured Company
				URL:     urlA,
			}},
		}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	input := engine.JobSearchInput{Query: "f3-precedence-unique", Platform: "all"}

	_, out, err := runJobSearch(ctx, nil, input)
	if err != nil {
		t.Fatalf("runJobSearch returned error: %v", err)
	}
	if len(out.Jobs) != 1 {
		t.Fatalf("F3 FAIL: len(out.Jobs) = %d, want 1", len(out.Jobs))
	}
	// Structured value wins where present.
	if out.Jobs[0].Title != "Structured Title" {
		t.Errorf("F3: Title = %q, want %q (structured value must win over LLM)", out.Jobs[0].Title, "Structured Title")
	}
	// LLM value fills where structured is empty.
	if out.Jobs[0].Company != "LLM Co" {
		t.Errorf("F3: Company = %q, want %q (LLM must fill empty structured field)", out.Jobs[0].Company, "LLM Co")
	}
}

// F4 — Ordering follows the gate score descending, and relevance is populated
// on each listing. The gate is degraded in tests (no embed client), so the
// Score is whatever the source set on the SearxngResult — it survives the
// degraded gate unchanged. Results are inserted LOW-first to prove the sort
// reorders them.
//
// Mutation: tool_job_search.go, delete the sort.SliceStable call -> order stays
// insertion (low score first) -> out.Jobs[0].Relevance < out.Jobs[1].Relevance -> RED.
func TestSpine_F4_OrderedByRelevanceDesc(t *testing.T) {
	const urlLow = "https://jobs.lever.co/testco/low"
	const urlHigh = "https://jobs.greenhouse.io/testco/high"
	src := testStructuredSource{
		results: []engine.SearxngResult{
			{URL: urlLow, Title: "Low Match", Content: "** Low Match at TestCo", Score: 0.5},
			{URL: urlHigh, Title: "High Match", Content: "** High Match at TestCo", Score: 0.9},
		},
		listings: []engine.JobListing{
			{URL: urlLow, Title: "Low Match", Source: "lever"},
			{URL: urlHigh, Title: "High Match", Source: "greenhouse"},
		},
	}
	withTestRegistry(t, src)

	stubSummarize(t, func(_ context.Context, query, _ string, _ int, _ []engine.SearxngResult, _ map[string]string) (*engine.JobSearchOutput, error) {
		return &engine.JobSearchOutput{Query: query, Summary: "2 results", Jobs: []engine.JobListing{
			{URL: urlLow, Title: "Low Match"},
			{URL: urlHigh, Title: "High Match"},
		}}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	input := engine.JobSearchInput{Query: "f4-ordering-unique", Platform: "all"}

	_, out, err := runJobSearch(ctx, nil, input)
	if err != nil {
		t.Fatalf("runJobSearch returned error: %v", err)
	}
	if len(out.Jobs) != 2 {
		t.Fatalf("F4 FAIL: len(out.Jobs) = %d, want 2", len(out.Jobs))
	}
	// Ordered by relevance descending.
	if out.Jobs[0].Relevance <= out.Jobs[1].Relevance {
		t.Fatalf("F4: out.Jobs[0].Relevance (%f) must be > out.Jobs[1].Relevance (%f) — ordered desc", out.Jobs[0].Relevance, out.Jobs[1].Relevance)
	}
	// Relevance is populated (the gate's score carried onto the listing).
	if out.Jobs[0].Relevance != 0.9 {
		t.Errorf("F4: out.Jobs[0].Relevance = %f, want 0.9 (high-score listing must be first)", out.Jobs[0].Relevance)
	}
	if out.Jobs[1].Relevance != 0.5 {
		t.Errorf("F4: out.Jobs[1].Relevance = %f, want 0.5", out.Jobs[1].Relevance)
	}
}

// F5 — The LLM-unavailable path does not write the cache. A 529 must not
// poison the cache for the next caller.
//
// Mutation: tool_job_search.go, in the `if len(finalJobs) > 0` unavailable
// branch, add `engine.CacheStoreJSON(ctx, cacheKey, input.Query, out)` before
// the return -> cache hit -> RED.
func TestSpine_F5_LLMUnavailableNotCached(t *testing.T) {
	const urlA = "https://jobs.lever.co/testco/aaa"
	src := testStructuredSource{
		results:  []engine.SearxngResult{{URL: urlA, Title: "Go Backend", Content: "** Go Backend at TestCo"}},
		listings: []engine.JobListing{{URL: urlA, Title: "Go Backend", Company: "TestCo", Source: "lever"}},
	}
	withTestRegistry(t, src)

	stubSummarize(t, func(_ context.Context, _, _ string, _ int, _ []engine.SearxngResult, _ map[string]string) (*engine.JobSearchOutput, error) {
		return nil, &llm.APIError{StatusCode: 529, Body: "Overloaded"}
	})

	// L1-only in-memory cache.
	engine.InitCache("", engine.CacheTTL, 64, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	input := engine.JobSearchInput{Query: "f5-cache-skip-unique", Platform: "all"}

	_, out, err := runJobSearch(ctx, nil, input)
	if err != nil {
		t.Fatalf("runJobSearch returned error: %v", err)
	}
	if len(out.Jobs) != 1 {
		t.Fatalf("F5 setup: len(out.Jobs) = %d, want 1 (structured must be served)", len(out.Jobs))
	}

	cacheKey := engine.CacheKey("job_search", input.Query, input.Location, input.Experience,
		input.JobType, input.Remote, input.TimeRange, input.Platform,
		fmt.Sprintf("limit_%d_offset_%d", input.Limit, input.Offset))
	if cached, ok := engine.CacheLoadJSON[engine.JobSearchOutput](ctx, cacheKey); ok {
		t.Errorf("F5 FAIL: LLM-unavailable result must NOT be cached; got a hit: %+v", cached)
	}
}

// F6 — A HEALTHY LLM that legitimately found nothing (Jobs: nil, no error,
// not Unparseable) is NOT unavailable. The LLM's empty list is the selection
// set: structured listings the LLM did NOT select must NOT be served. The
// output is the honest-empty path (len 0, honest summary), and the summary
// must NOT claim the prose is "unavailable".
//
// Mutation: tool_job_search.go, reinstate the `no_jobs` arm
// (`if !llmUnavailable && jobOut != nil && len(jobOut.Jobs) == 0 { llmUnavailable = true; llmErrClass = "no_jobs" }`)
// -> the 2 structured candidates get served via the unavailable path ->
// len(out.Jobs) == 2 and summary contains "unavailable" -> RED.
func TestSpine_F6_HealthyLLMNoJobsIsHonestEmpty(t *testing.T) {
	const urlA = "https://jobs.lever.co/testco/java-dev"
	const urlB = "https://jobs.greenhouse.io/testco/dotnet-dev"
	src := testStructuredSource{
		results: []engine.SearxngResult{
			{URL: urlA, Title: "Java Developer", Content: "** Java at TestCo"},
			{URL: urlB, Title: ".NET Developer", Content: "** .NET at TestCo"},
		},
		listings: []engine.JobListing{
			{URL: urlA, Title: "Java Developer", Company: "TestCo", Source: "lever"},
			{URL: urlB, Title: ".NET Developer", Company: "TestCo", Source: "greenhouse"},
		},
	}
	withTestRegistry(t, src)

	stubSummarize(t, func(_ context.Context, query, _ string, _ int, _ []engine.SearxngResult, _ map[string]string) (*engine.JobSearchOutput, error) {
		// Healthy LLM, honest "no match": nil Jobs, a real summary, not Unparseable.
		return &engine.JobSearchOutput{Query: query, Summary: "No matching roles found for this query."}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	input := engine.JobSearchInput{Query: "f6-healthy-empty-unique", Platform: "all"}

	_, out, err := runJobSearch(ctx, nil, input)
	if err != nil {
		t.Fatalf("runJobSearch returned error: %v", err)
	}
	if len(out.Jobs) != 0 {
		t.Fatalf("F6 FAIL: len(out.Jobs) = %d, want 0 (healthy LLM's empty selection must not serve unselected structured listings)", len(out.Jobs))
	}
	if contains(out.Summary, "unavailable") {
		t.Errorf("F6 FAIL: a healthy LLM's honest-empty summary must NOT say 'unavailable'; got: %s", out.Summary)
	}
}

// F7 — A HEALTHY LLM that selects 1 of 2 structured candidates: the LLM's
// list is the SELECTION SET. Only the selected structured listing is served
// (with empty fields filled from the LLM listing); the rejected structured
// listing is ABSENT.
//
// Mutation: tool_job_search.go, reinstate the unconditional union build
// (iterate `top` and emit every structured listing regardless of LLM
// selection) -> the rejected Java listing is appended -> len == 2 -> RED.
func TestSpine_F7_HealthyLLMSelectionDropsUnselected(t *testing.T) {
	const urlRust = "https://jobs.lever.co/testco/rust-dev"
	const urlJava = "https://jobs.greenhouse.io/testco/java-dev"
	src := testStructuredSource{
		results: []engine.SearxngResult{
			{URL: urlRust, Title: "Rust Developer", Content: "** Rust at TestCo"},
			{URL: urlJava, Title: "Java Developer", Content: "** Java at TestCo"},
		},
		listings: []engine.JobListing{
			{URL: urlRust, Title: "Rust Developer", Company: "TestCo", Source: "lever"},
			{URL: urlJava, Title: "Java Developer", Company: "TestCo", Source: "greenhouse"},
		},
	}
	withTestRegistry(t, src)

	stubSummarize(t, func(_ context.Context, query, _ string, _ int, _ []engine.SearxngResult, _ map[string]string) (*engine.JobSearchOutput, error) {
		// Healthy LLM selects ONLY the Rust role.
		return &engine.JobSearchOutput{
			Query:   query,
			Summary: "One Rust role found.",
			Jobs:    []engine.JobListing{{Title: "Rust Developer", Company: "TestCo", URL: urlRust}},
		}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	input := engine.JobSearchInput{Query: "f7-selection-unique", Platform: "all"}

	_, out, err := runJobSearch(ctx, nil, input)
	if err != nil {
		t.Fatalf("runJobSearch returned error: %v", err)
	}
	if len(out.Jobs) != 1 {
		t.Fatalf("F7 FAIL: len(out.Jobs) = %d, want 1 (LLM selected 1 of 2; the rejected structured listing must be absent)", len(out.Jobs))
	}
	if out.Jobs[0].URL != urlRust {
		t.Errorf("F7 FAIL: out.Jobs[0].URL = %q, want %q (the selected listing)", out.Jobs[0].URL, urlRust)
	}
	for _, j := range out.Jobs {
		if j.URL == urlJava {
			t.Errorf("F7 FAIL: rejected structured listing %q must be ABSENT from the output", urlJava)
		}
	}
}
