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
	const urlA = "https://jobs.lever.co/testco/aaa"         // structured-backed
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

// F8 — A healthy LLM listing whose URL does NOT normalize-match its structured
// counterpart, but whose JobID and Source DO match → the structured fields win.
// The JobID fallback in jobs.StructuredMatcher.Match (called from
// buildHealthySelection) resolves the join. This is the #418 defect: a healthy
// LLM that emits a slightly different URL for the same Lever/Greenhouse/Ashby
// posting silently got no structured match and shipped the LLM's guessed
// salary instead of the API's numbers.
//
// Mutation: in jobs.StructuredMatcher.Match (ats.go), delete the JobID fallback
// arm (the `if id != "" { ... }` block) → no match → LLM listing emitted
// unchanged → SalaryMin stays nil → RED.
func TestSpine_F8_JobIDFallbackHealthyPath(t *testing.T) {
	minSalary := 160000
	structuredByURL := map[string]engine.JobListing{
		"https://jobs.lever.co/testco/abc": {
			URL:       "https://jobs.lever.co/testco/abc",
			JobID:     "abc",
			SalaryMin: &minSalary,
			Source:    "lever",
		},
	}
	llmJobs := []engine.JobListing{
		{
			// Different URL (no normalize match), same JobID + Source.
			URL:       "https://jobs.lever.co/testco/abc/apply",
			JobID:     "abc",
			Source:    "lever",
			Title:     "Eng",
			SalaryMin: nil,
		},
	}
	scoreByURL := map[string]float64{}
	seen := map[string]bool{}

	out := buildHealthySelection(llmJobs, structuredByURL, scoreByURL, seen)

	if len(out) != 1 {
		t.Fatalf("F8 FAIL: len(out) = %d, want 1", len(out))
	}
	if out[0].SalaryMin == nil || *out[0].SalaryMin != 160000 {
		t.Errorf("F8 FAIL: SalaryMin = %v, want 160000 (JobID fallback must match the structured listing; the LLM's nil must not ship)", out[0].SalaryMin)
	}
}

// F9 — Cross-provider JobID collision: an LLM record with a LinkedIn-shaped id
// and no Source, and a Greenhouse structured candidate carrying the same id
// string → the JobID fallback must REFUSE (source-equality guard) and the
// records must NOT merge. This is the reason the guard exists.
//
// Mutation: in jobs.StructuredMatcher.Match (ats.go), drop the
// `llmSrc != "" && cand.Source == llmSrc` condition (match on JobID alone) →
// the Greenhouse candidate wrongly matches → structured listing emitted with
// Title "Greenhouse Eng" → RED.
func TestSpine_F9_CrossProviderCollisionRefused(t *testing.T) {
	structuredByURL := map[string]engine.JobListing{
		"https://boards.greenhouse.io/testco/jobs/4001234": {
			URL:    "https://boards.greenhouse.io/testco/jobs/4001234",
			JobID:  "4001234",
			Title:  "Greenhouse Eng",
			Source: "greenhouse",
		},
	}
	llmJobs := []engine.JobListing{
		{
			// LinkedIn record — URL does NOT normalize-match the Greenhouse URL,
			// so the JobID fallback is the only path. Source is EMPTY; the URL
			// resolves to "" via extractSourceFromURL (LinkedIn is not an ATS)
			// → llmSrc stays "" → fallback refused.
			URL:     "https://www.linkedin.com/jobs/view/4001234",
			JobID:   "4001234",
			Source:  "",
			Title:   "LinkedIn Eng",
			Company: "LinkedInCorp",
		},
	}
	scoreByURL := map[string]float64{}
	seen := map[string]bool{}

	out := buildHealthySelection(llmJobs, structuredByURL, scoreByURL, seen)

	if len(out) != 1 {
		t.Fatalf("F9 FAIL: len(out) = %d, want 1", len(out))
	}
	if out[0].Title != "LinkedIn Eng" {
		t.Errorf("F9 FAIL: Title = %q, want %q (cross-provider JobID collision must NOT rewrite the LLM record)", out[0].Title, "LinkedIn Eng")
	}
	if out[0].Source == "greenhouse" {
		t.Errorf("F9 FAIL: Source = %q, must NOT be relabelled greenhouse (cross-provider collision refused)", out[0].Source)
	}
}

// F10 — Unavailable path with BOTH structured and LLM-only listings → the
// summary reports the two counts separately and does NOT claim all are
// machine-extracted. The LLM returned an unparseable response that still
// carried one LLM-only job (no structured counterpart); one structured listing
// survived the gate. The summary must name both counts.
//
// Mutation: in the summary switch (tool_job_search.go), revert to the single
// "served from machine-extracted structured sources" claim ignoring nLLMOnly →
// summary overclaims (says "machine-extracted" for an LLM-only listing) → RED.
func TestSpine_F10_UnavailableSummaryReportsBothCounts(t *testing.T) {
	const urlStructured = "https://jobs.lever.co/testco/aaa"      // structured-backed
	const urlLLMOnly = "https://news.ycombinator.com/item?id=999" // LLM-only
	src := testStructuredSource{
		results: []engine.SearxngResult{
			{URL: urlStructured, Title: "Go Backend", Content: "** Go Backend at TestCo"},
		},
		listings: []engine.JobListing{
			{URL: urlStructured, Title: "Go Backend", Company: "TestCo", Source: "lever"},
		},
	}
	withTestRegistry(t, src)

	// Unparseable response that still carries one LLM-only job — exercises the
	// LLM-only append arm of buildUnavailableSpine (which production
	// SummarizeJobResults does not populate today, but the code path must stay
	// honest if it ever does).
	stubSummarize(t, func(_ context.Context, query, _ string, _ int, _ []engine.SearxngResult, _ map[string]string) (*engine.JobSearchOutput, error) {
		return &engine.JobSearchOutput{
			Query:       query,
			Summary:     "1 result",
			Unparseable: true,
			Jobs: []engine.JobListing{
				{Title: "HN Text Post", Company: "Startup", URL: urlLLMOnly},
			},
		}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	input := engine.JobSearchInput{Query: "f10-summary-both-counts-unique", Platform: "all"}

	_, out, err := runJobSearch(ctx, nil, input)
	if err != nil {
		t.Fatalf("runJobSearch returned error: %v", err)
	}
	if len(out.Jobs) != 2 {
		t.Fatalf("F10 setup: len(out.Jobs) = %d, want 2 (1 structured + 1 LLM-only)", len(out.Jobs))
	}
	// The summary must state BOTH counts and must NOT claim every listing is
	// machine-extracted (the LLM-only listing is not).
	if !contains(out.Summary, "1 machine-extracted structured") {
		t.Errorf("F10 FAIL: summary must name the structured count; got: %s", out.Summary)
	}
	if !contains(out.Summary, "1 LLM-extracted") {
		t.Errorf("F10 FAIL: summary must name the LLM-only count separately; got: %s", out.Summary)
	}
}

// F11 — LLM errors, zero structured survive → job_search_extraction_total is
// incremented. Previously IncrJobSearchExtraction("llm_unavailable") was only
// reached when structured listings survived, so an LLM error with zero
// survivors incremented no series at all and the LLM-failure rate read
// systematically low.
//
// Mutation: in tool_job_search.go, move the IncrJobSearchExtraction call back
// inside the `if len(finalJobs) > 0` block → the empty-error path increments
// nothing → delta stays 0 → RED.
func TestSpine_F11_LLMErrorZeroStructuredIncrementsCounter(t *testing.T) {
	// A non-structured source so no structured listings survive the gate.
	src := testResultSource{results: []engine.SearxngResult{
		{URL: "http://example.com/job-1", Title: "Go Dev", Content: "**Source:** test"},
	}}
	withTestRegistry(t, src)

	stubSummarize(t, func(_ context.Context, _, _ string, _ int, _ []engine.SearxngResult, _ map[string]string) (*engine.JobSearchOutput, error) {
		return nil, &llm.APIError{StatusCode: 529, Body: "Overloaded"}
	})

	engine.InitTestRegistry()
	key := engine.MetricJobSearchExtraction + "{outcome=llm_unavailable}"
	before := engine.GetMetrics()[key]

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	input := engine.JobSearchInput{Query: "f11-counter-empty-error-unique", Platform: "all"}

	_, out, err := runJobSearch(ctx, nil, input)
	if err != nil {
		t.Fatalf("runJobSearch returned error: %v", err)
	}
	// Sanity: this is the empty-error path (no structured survived).
	if len(out.Jobs) != 0 {
		t.Fatalf("F11 setup: len(out.Jobs) = %d, want 0 (no structured survived)", len(out.Jobs))
	}

	after := engine.GetMetrics()[key]
	if delta := after - before; delta != 1 {
		t.Errorf("F11 FAIL: job_search_extraction_total{outcome=llm_unavailable} delta = %d, want 1 (LLM error with zero survivors must still increment the counter)", delta)
	}
}

// --- PORTED invariants from the retired ApplyStructuredPrecedence suite ---
//
// The following tests (P1–P5) re-express load-bearing invariants that
// ApplyStructuredPrecedence encoded against the OLD direction (LLM-is-spine,
// structured overrides fields). The live path inverts that: structured is the
// spine (buildHealthySelection), and FillStructuredFromLLM fills LLM values
// only into empty structured gaps. Each test drives buildHealthySelection
// directly — the same seam F8/F9 use.

// P1 — Normalized-URL keying: an LLM URL with a trailing slash + query param
// still matches a structured listing with a clean URL. Without NormalizeURL on
// both sides, structuredByURL is an exact-string map and a single trailing
// slash yields zero hits (the HIGH finding).
//
// Mutation: in jobs.NewStructuredMatcher (ats.go), replace `NormalizeURL(s.URL)`
// with `s.URL` → no match → LLM emitted unchanged → SalaryMin nil → RED.
func TestSpine_P1_URLNormalizationMatch(t *testing.T) {
	min := 160000
	max := 220000
	structuredByURL := map[string]engine.JobListing{
		"https://jobs.lever.co/testco/abc": {
			URL:            "https://jobs.lever.co/testco/abc",
			SalaryMin:      &min,
			SalaryMax:      &max,
			SalaryCurrency: "USD",
			SalaryInterval: "year",
			Salary:         "160000–220000 USD/year",
			Source:         "lever",
		},
	}
	llmJobs := []engine.JobListing{
		{
			// Trailing slash + query — the exact variation that produced zero
			// hits before NormalizeURL was added to both sides.
			URL:       "https://jobs.lever.co/testco/abc/?source=llm",
			Title:     "Eng",
			SalaryMin: nil,
		},
	}
	out := buildHealthySelection(llmJobs, structuredByURL, map[string]float64{}, map[string]bool{})
	if len(out) != 1 {
		t.Fatalf("P1 FAIL: len(out) = %d, want 1", len(out))
	}
	if out[0].SalaryMin == nil || *out[0].SalaryMin != 160000 {
		t.Errorf("P1 FAIL: SalaryMin = %v, want 160000 (normalized URL must match despite trailing slash + query)", out[0].SalaryMin)
	}
	if out[0].Salary != "160000–220000 USD/year" {
		t.Errorf("P1 FAIL: Salary = %q, want structured string (match via normalized URL)", out[0].Salary)
	}
}

// P2 — Salary-group coherence guard: an Ashby structured listing carries
// free-text Salary (compensationTierSummary) but nil numerics. When joined to
// an LLM listing with a coherent numeric group, the LLM numerics must NOT be
// grafted onto the structured free-text — that would pair structured free-text
// with LLM-guessed numerics, a self-contradictory record neither source
// produced. FillStructuredFromLLM's guard (s.Salary == "" gate on numeric fill)
// blocks this. The structured free-text Salary is authoritative; the LLM
// numerics are dropped. This replaces both AshbyNumericsPreserved and
// AshbySalaryDisagreementKeepsLLMGroup from the old suite — the invariant is
// the same (don't mix structured free-text with LLM numerics), but the
// direction inverted: the old path kept the LLM group and dropped structured
// free-text; the live path keeps structured free-text and drops LLM numerics.
//
// Mutation: in jobs.FillStructuredFromLLM (ats.go), remove the `s.Salary == ""`
// guard on the SalaryMin fill → LLM numerics grafted → SalaryMin=180000 → RED.
func TestSpine_P2_AshbyCoherenceGuardBlocksLLMNumerics(t *testing.T) {
	structuredByURL := map[string]engine.JobListing{
		"https://jobs.ashbyhq.com/testco/abc": {
			URL:    "https://jobs.ashbyhq.com/testco/abc",
			Title:  "Eng",
			Source: "ashby",
			Salary: "$300k–$400k USD", // compensationTierSummary, free-text
			// SalaryMin/Max/Currency/Interval intentionally nil/"" — ashbyJobToListing
			// never sets the numeric fields.
		},
	}
	llmMin := 180000
	llmMax := 220000
	llmJobs := []engine.JobListing{
		{
			URL:            "https://jobs.ashbyhq.com/testco/abc",
			Salary:         "180000-220000 USD",
			SalaryMin:      &llmMin,
			SalaryMax:      &llmMax,
			SalaryCurrency: "USD",
			SalaryInterval: "year",
		},
	}
	out := buildHealthySelection(llmJobs, structuredByURL, map[string]float64{}, map[string]bool{})
	if len(out) != 1 {
		t.Fatalf("P2 FAIL: len(out) = %d, want 1", len(out))
	}
	// Structured free-text Salary is authoritative; LLM Salary NOT grafted.
	if out[0].Salary != "$300k–$400k USD" {
		t.Errorf("P2 FAIL: Salary = %q, want structured free-text (authoritative — LLM Salary not grafted onto structured free-text)", out[0].Salary)
	}
	// LLM numerics NOT grafted — coherence guard blocks the fill.
	if out[0].SalaryMin != nil {
		t.Errorf("P2 FAIL: SalaryMin = %v, want nil (LLM numerics must NOT be grafted onto structured free-text — coherence guard)", out[0].SalaryMin)
	}
	if out[0].SalaryMax != nil {
		t.Errorf("P2 FAIL: SalaryMax = %v, want nil (coherence guard)", out[0].SalaryMax)
	}
}

// P3 / F13 — Structured listing with NO salary of any kind + LLM carrying
// free-text AND numerics from the same record → all of Salary, SalaryMin,
// SalaryMax survive on the output. A Greenhouse structured listing has no comp
// field (the API carries none). When joined to an LLM listing with a coherent
// salary group (free-text + numerics + currency + interval), FillStructuredFromLLM
// fills ALL the gaps — the LLM salary STRING and the LLM numerics both reach the
// output, so a salary_min filter (hunt/store.go) can match the job.
//
// The guard captures whether the STRUCTURED listing carried free-text salary
// BEFORE any fill, and guards the numerics on that captured value — not on
// s.Salary which the Salary fill just mutated. The old order-dependent guard
// (s.Salary == "" checked AFTER the Salary fill) blocked the numerics because
// s.Salary was now non-empty; the fix captures structuredHadSalary before the
// fill so LLM-on-LLM coherence (same source, same record) is preserved.
//
// Mutation: restore the order-dependent guard (guard numerics on s.Salary
// instead of the captured structuredHadSalary) → s.Salary is non-empty after
// the fill → numerics nil → RED.
func TestSpine_P3_GreenhouseSalaryFillFromLLM(t *testing.T) {
	structuredByURL := map[string]engine.JobListing{
		"https://boards.greenhouse.io/testco/jobs/4001234": {
			URL:     "https://boards.greenhouse.io/testco/jobs/4001234",
			Title:   "Backend Engineer",
			Company: "testco",
			Source:  "greenhouse",
			// Salary absent — Greenhouse API has no comp field.
		},
	}
	llmMin := 160000
	llmMax := 220000
	llmJobs := []engine.JobListing{
		{
			URL:            "https://boards.greenhouse.io/testco/jobs/4001234",
			Salary:         "160000-220000 USD",
			SalaryMin:      &llmMin,
			SalaryMax:      &llmMax,
			SalaryCurrency: "USD",
			SalaryInterval: "year",
		},
	}
	out := buildHealthySelection(llmJobs, structuredByURL, map[string]float64{}, map[string]bool{})
	if len(out) != 1 {
		t.Fatalf("P3 FAIL: len(out) = %d, want 1", len(out))
	}
	// LLM Salary string fills the structured gap (structured has no comp field).
	if out[0].Salary != "160000-220000 USD" {
		t.Errorf("P3 FAIL: Salary = %q, want LLM value (structured has no comp — LLM fills gap)", out[0].Salary)
	}
	// F13: LLM numerics MUST survive — the guard captures structuredHadSalary
	// before the fill, so the Salary fill does not block the numeric fill.
	if out[0].SalaryMin == nil || *out[0].SalaryMin != 160000 {
		t.Errorf("P3 FAIL: SalaryMin = %v, want 160000 (LLM numerics must survive when structured has no salary — guard on captured structuredHadSalary, not mutated s.Salary)", out[0].SalaryMin)
	}
	if out[0].SalaryMax == nil || *out[0].SalaryMax != 220000 {
		t.Errorf("P3 FAIL: SalaryMax = %v, want 220000 (same guard fix)", out[0].SalaryMax)
	}
	if out[0].SalaryCurrency != "USD" {
		t.Errorf("P3 FAIL: SalaryCurrency = %q, want USD", out[0].SalaryCurrency)
	}
	if out[0].SalaryInterval != "year" {
		t.Errorf("P3 FAIL: SalaryInterval = %q, want year", out[0].SalaryInterval)
	}
	// Structured non-salary fields win.
	if out[0].Title != "Backend Engineer" {
		t.Errorf("P3 FAIL: Title = %q, want structured value", out[0].Title)
	}
	if out[0].Company != "testco" {
		t.Errorf("P3 FAIL: Company = %q, want structured value", out[0].Company)
	}
}

// P4 — Empty-Source JobID fallback matches same provider: an LLM record with no
// Source but a Lever URL + JobID still matches a Lever structured candidate.
// StructuredMatcher.Match resolves the empty Source from the URL via
// extractSourceFromURL and requires equality with the candidate's Source. F9
// covers the REFUSE half (non-ATS URL → resolved Source is "" → fallback
// refused); P4 covers the MATCH half (ATS URL → resolved Source == candidate
// Source → fallback accepted).
//
// Mutation: in jobs.StructuredMatcher.Match (ats.go), refuse the JobID fallback
// when llm.Source == "" (remove the extractSourceFromURL resolution, treat
// empty as always-mismatch) → no match → SalaryMin nil → RED.
func TestSpine_P4_EmptySourceJobIDFallbackMatchesSameProvider(t *testing.T) {
	min := 160000
	structuredByURL := map[string]engine.JobListing{
		"https://jobs.lever.co/testco/abc": {
			URL:       "https://jobs.lever.co/testco/abc",
			JobID:     "abc",
			SalaryMin: &min,
			Source:    "lever",
		},
	}
	llmJobs := []engine.JobListing{
		{
			// Different URL (no normalize match), same JobID, no Source.
			// The URL resolves to "lever" via extractSourceFromURL.
			URL:       "https://jobs.lever.co/testco/abc/apply",
			JobID:     "abc",
			Source:    "",
			Title:     "Eng",
			SalaryMin: nil,
		},
	}
	out := buildHealthySelection(llmJobs, structuredByURL, map[string]float64{}, map[string]bool{})
	if len(out) != 1 {
		t.Fatalf("P4 FAIL: len(out) = %d, want 1", len(out))
	}
	if out[0].SalaryMin == nil || *out[0].SalaryMin != 160000 {
		t.Errorf("P4 FAIL: SalaryMin = %v, want 160000 (empty-Source same-provider JobID fallback must match via URL resolution)", out[0].SalaryMin)
	}
}

// P5 — First-write-wins on duplicate normalized URLs: two structured listings
// whose URLs normalize to the same key (trailing slash on the second) →
// NewStructuredMatcher keeps the first inserted. Map iteration order is
// non-deterministic, so the test asserts a match happened (Title is one of the
// two structured values, not the LLM value), not which specific one.
//
// Mutation: in jobs.NewStructuredMatcher (ats.go), delete the byNormURL
// population loop → no URL match, byJobID also empty → no match → Title stays
// "LLM" → RED.
func TestSpine_P5_FirstWriteWinsOnDuplicateNormURL(t *testing.T) {
	structuredByURL := map[string]engine.JobListing{
		"https://jobs.lever.co/testco/abc": {
			URL:    "https://jobs.lever.co/testco/abc",
			Title:  "First",
			Source: "lever",
		},
		"https://jobs.lever.co/testco/abc/": {
			URL:    "https://jobs.lever.co/testco/abc/",
			Title:  "Second",
			Source: "lever",
		},
	}
	llmJobs := []engine.JobListing{
		{URL: "https://jobs.lever.co/testco/abc", Title: "LLM"},
	}
	out := buildHealthySelection(llmJobs, structuredByURL, map[string]float64{}, map[string]bool{})
	if len(out) != 1 {
		t.Fatalf("P5 FAIL: len(out) = %d, want 1", len(out))
	}
	if out[0].Title != "First" && out[0].Title != "Second" {
		t.Errorf("P5 FAIL: Title = %q, want one of the two structured titles (first-write-wins dedup must have matched)", out[0].Title)
	}
}

// F12 — Cross-provider JobID collision between two RESOLVABLE ATS sources: an
// LLM record with a Lever URL (no Source field) and a Greenhouse structured
// candidate carrying the same JobID string. The URLs do not normalize-match, so
// the JobID fallback is the only path, and this time llmSrc RESOLVES ("lever")
// — so the refusal can only come from the source-EQUALITY arm of the guard.
//
// F9 covers the other arm: an unresolvable source (LinkedIn) leaves llmSrc empty
// and the fallback is refused by `llmSrc != ""` alone. Dropping only
// `cand.Source == llmSrc` leaves F9 green, so without F12 half the guard is
// untested and can be deleted silently.
//
// Mutation: in jobs.StructuredMatcher.Match (ats.go), drop `cand.Source ==
// llmSrc` from the condition (keep `llmSrc != ""`) → the Greenhouse
// candidate wrongly matches the Lever record → Title becomes "Greenhouse Eng"
// → RED.
func TestSpine_F12_CrossProviderCollisionResolvableSources(t *testing.T) {
	structuredByURL := map[string]engine.JobListing{
		"https://boards.greenhouse.io/testco/jobs/4001234": {
			URL:    "https://boards.greenhouse.io/testco/jobs/4001234",
			JobID:  "4001234",
			Title:  "Greenhouse Eng",
			Source: "greenhouse",
		},
	}
	llmJobs := []engine.JobListing{
		{
			// Lever URL — resolves to "lever" via extractSourceFromURL, so
			// llmSrc is non-empty and only the equality arm can refuse.
			URL:     "https://jobs.lever.co/testco/4001234",
			JobID:   "4001234",
			Source:  "",
			Title:   "Lever Eng",
			Company: "LeverCorp",
		},
	}

	out := buildHealthySelection(llmJobs, structuredByURL, map[string]float64{}, map[string]bool{})

	if len(out) != 1 {
		t.Fatalf("F12 FAIL: len(out) = %d, want 1", len(out))
	}
	if out[0].Title != "Lever Eng" {
		t.Errorf("F12 FAIL: Title = %q, want %q (a resolvable cross-provider JobID collision must NOT merge)", out[0].Title, "Lever Eng")
	}
	if out[0].Source == "greenhouse" {
		t.Errorf("F12 FAIL: Source = %q, must NOT be relabelled greenhouse", out[0].Source)
	}
}

// F17 — Salary-group coherence covers the WHOLE group, not just the numerics.
// When the structured listing carried free-text Salary (the Ashby case:
// compensationTierSummary is authoritative, the LLM's numbers are a guess), the
// LLM's SalaryMin/Max are refused — but SalaryCurrency and SalaryInterval were
// still filled, so the output paired an authoritative structured salary string
// with currency and interval describing numerics that had just been thrown
// away. That is the same self-contradictory record the guard exists to prevent,
// leaking through two fields nobody guarded.
//
// Mutation: in FillStructuredFromLLM (ats.go), drop !structuredHadSalary from
// the SalaryCurrency / SalaryInterval conditions -> EUR/month leak onto a
// structured USD-range string -> RED.
func TestSpine_F17_CoherenceGuardCoversCurrencyAndInterval(t *testing.T) {
	llmMin, llmMax := 100000, 120000
	structuredByURL := map[string]engine.JobListing{
		"https://jobs.ashbyhq.com/testco/abc": {
			URL:    "https://jobs.ashbyhq.com/testco/abc",
			Title:  "Eng",
			Source: "ashby",
			// compensationTierSummary — authoritative free text, no numerics,
			// no currency, no interval. Exactly what Ashby publishes.
			Salary: "USD 200,000 - 250,000 per year + equity",
		},
	}
	llmJobs := []engine.JobListing{{
		URL:            "https://jobs.ashbyhq.com/testco/abc",
		Title:          "Eng",
		SalaryMin:      &llmMin,
		SalaryMax:      &llmMax,
		SalaryCurrency: "EUR",
		SalaryInterval: "month",
	}}

	out := buildHealthySelection(llmJobs, structuredByURL, map[string]float64{}, map[string]bool{})
	if len(out) != 1 {
		t.Fatalf("F17 FAIL: len(out) = %d, want 1", len(out))
	}
	j := out[0]
	if j.SalaryMin != nil || j.SalaryMax != nil {
		t.Errorf("F17 FAIL: numerics leaked (min=%v max=%v); structured free-text must refuse them", j.SalaryMin, j.SalaryMax)
	}
	if j.SalaryCurrency != "" {
		t.Errorf("F17 FAIL: SalaryCurrency = %q, want empty — it describes the refused LLM numerics, not the structured string", j.SalaryCurrency)
	}
	if j.SalaryInterval != "" {
		t.Errorf("F17 FAIL: SalaryInterval = %q, want empty — it describes the refused LLM numerics, not the structured string", j.SalaryInterval)
	}
}
