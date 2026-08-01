package jobserver

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/anatolykoptev/go-kit/breaker"
	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/anatolykoptev/go_job/internal/engine/jobs/connectors"
)

// TestClassifySourceResult_ErrNoAPIKey_Skipped: a source returning ErrNoAPIKey
// is classified as "skipped" and the zero-results summary names it (does NOT
// equal the generic "No results found.").
//
// Revert-red: if classifySourceResult stops special-casing ErrNoAPIKey it falls
// through to "failed" and the outcome assertion fails.
func TestClassifySourceResult_ErrNoAPIKey_Skipped(t *testing.T) {
	r := sourceResult{name: "indeed", err: fmt.Errorf("indeed: no key: %w", jobs.ErrNoAPIKey)}
	st := classifySourceResult(r)
	if st.Outcome != engine.SourceOutcomeSkipped {
		t.Fatalf("outcome = %q, want %q", st.Outcome, engine.SourceOutcomeSkipped)
	}
	if st.Name != "indeed" {
		t.Errorf("name = %q, want indeed", st.Name)
	}

	summary := buildZeroResultsSummary([]engine.SourceStatus{st})
	if summary == "No results found." {
		t.Fatal("summary must name the skipped source, not the generic 'No results found.'")
	}
	if !contains(summary, "indeed") {
		t.Errorf("summary must name the skipped source; got: %s", summary)
	}
}

// TestClassifySourceResult_BreakerOpen_Blocked: a breaker-open error classifies
// as "blocked".
func TestClassifySourceResult_BreakerOpen_Blocked(t *testing.T) {
	r := sourceResult{name: "linkedin", err: fmt.Errorf("linkedin breaker open: %w", breaker.ErrOpen)}
	st := classifySourceResult(r)
	if st.Outcome != engine.SourceOutcomeBlocked {
		t.Fatalf("outcome = %q, want %q (err: %v)", st.Outcome, engine.SourceOutcomeBlocked, r.err)
	}
}

// TestClassifySourceResult_NilNil_Empty: a source returning (nil, nil) is
// "empty", and when it is the only source the summary stays the genuine
// "No results found." (that case IS a real empty, not a masked failure).
func TestClassifySourceResult_NilNil_Empty(t *testing.T) {
	r := sourceResult{name: "craigslist", results: nil, err: nil}
	st := classifySourceResult(r)
	if st.Outcome != engine.SourceOutcomeEmpty {
		t.Fatalf("outcome = %q, want %q", st.Outcome, engine.SourceOutcomeEmpty)
	}
	summary := buildZeroResultsSummary([]engine.SourceStatus{st})
	if summary != "No results found." {
		t.Fatalf("genuine empty-only summary must be 'No results found.', got: %s", summary)
	}
}

// TestClassifySourceResult_OK: results + nil err -> ok.
func TestClassifySourceResult_OK(t *testing.T) {
	r := sourceResult{name: "greenhouse", results: make([]engine.SearxngResult, 3), err: nil}
	st := classifySourceResult(r)
	if st.Outcome != engine.SourceOutcomeOK {
		t.Fatalf("outcome = %q, want %q", st.Outcome, engine.SourceOutcomeOK)
	}
	if st.Count != 3 {
		t.Errorf("count = %d, want 3", st.Count)
	}
}

// TestClassifySourceResult_DeadlineExceeded_Failed: context.DeadlineExceeded
// classifies as "failed".
func TestClassifySourceResult_DeadlineExceeded_Failed(t *testing.T) {
	r := sourceResult{name: "hn", err: context.DeadlineExceeded}
	st := classifySourceResult(r)
	if st.Outcome != engine.SourceOutcomeFailed {
		t.Fatalf("outcome = %q, want %q", st.Outcome, engine.SourceOutcomeFailed)
	}
}

// TestAggregateSourceResults_Cancellation_ReturnsPartialAndDoesNotHang:
// cancelling the context mid-aggregation returns the results already collected,
// marks the un-reported source as "failed" with a deadline reason, and does NOT
// hang. A real cancelled context is used; the test itself has a timeout so a
// regression (missing ctx.Done() select) fails loudly instead of hanging CI.
func TestAggregateSourceResults_Cancellation_ReturnsPartialAndDoesNotHang(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan sourceResult, 1) // no sender — the source never reports
	srcs := []connectors.Source{testSlowSource{sleep: time.Hour}}
	dispatched := map[string]bool{"test-slow": true} // was dispatched but won't report

	cancel() // cancel BEFORE calling so the ctx.Done() arm fires immediately

	done := make(chan struct{})
	var merged []engine.SearxngResult
	var sources []engine.SourceStatus
	var partial bool
	go func() {
		defer close(done)
		merged, _, _, sources, partial = aggregateSourceResults(ctx, srcs, false, ch, 1, dispatched)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("aggregateSourceResults hung on cancelled context — ctx.Done() select missing (regression)")
	}

	if len(merged) != 0 {
		t.Errorf("merged = %d, want 0 (no source reported)", len(merged))
	}
	if !partial {
		t.Error("partial = false, want true (source did not report)")
	}
	if len(sources) != 1 {
		t.Fatalf("sources = %d, want 1 (the un-reported source marked failed)", len(sources))
	}
	if sources[0].Name != "test-slow" {
		t.Errorf("source name = %q, want test-slow", sources[0].Name)
	}
	if sources[0].Outcome != engine.SourceOutcomeFailed {
		t.Errorf("outcome = %q, want %q", sources[0].Outcome, engine.SourceOutcomeFailed)
	}
	if sources[0].Reason == "" {
		t.Error("failed source must carry a deadline reason")
	}
}

// TestAggregateSourceResults_HappyPath_DrainsAndClassifies: a single reporting
// source is drained and classified; the loop terminates.
func TestAggregateSourceResults_HappyPath_DrainsAndClassifies(t *testing.T) {
	ctx := context.Background()
	ch := make(chan sourceResult, 1)
	srcs := []connectors.Source{testSlowSource{sleep: time.Hour}}
	dispatched := map[string]bool{"test-slow": true}
	ch <- sourceResult{name: "test-slow", results: make([]engine.SearxngResult, 2), err: nil}

	merged, _, _, sources, partial := aggregateSourceResults(ctx, srcs, false, ch, 1, dispatched)
	if len(merged) != 2 {
		t.Fatalf("merged = %d, want 2", len(merged))
	}
	if partial {
		t.Error("partial = true, want false (all sources reported)")
	}
	if len(sources) != 1 || sources[0].Outcome != engine.SourceOutcomeOK {
		t.Fatalf("sources = %+v, want one ok", sources)
	}
}

// TestAggregateSourceResults_GenericSearxngCancelled_MarkedFailed: when the
// generic searxng goroutine is enabled but does not report before cancellation,
// it is marked failed by name ("searxng").
func TestAggregateSourceResults_GenericSearxngCancelled_MarkedFailed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan sourceResult, 2)
	srcs := []connectors.Source{testSlowSource{sleep: time.Hour}}
	dispatched := map[string]bool{"test-slow": true, "searxng": true}
	cancel()

	done := make(chan struct{})
	var sources []engine.SourceStatus
	go func() {
		defer close(done)
		_, _, _, sources, _ = aggregateSourceResults(ctx, srcs, true, ch, 2, dispatched)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("hung — ctx.Done() select missing")
	}

	names := map[string]bool{}
	for _, s := range sources {
		names[s.Name] = true
		if s.Outcome != engine.SourceOutcomeFailed {
			t.Errorf("source %q outcome = %q, want failed", s.Name, s.Outcome)
		}
	}
	if !names["test-slow"] || !names["searxng"] {
		t.Errorf("expected both test-slow and searxng marked failed; got %v", names)
	}
}

// TestAggregateSourceResults_PriorityDrain_BufferedResultNotDroppedOnCancellation
// is the BLOCKER 1 regression test. Go's select is uniform-random among ready
// arms. With a fair select and no drain protection, a buffered result +
// cancelled context drops the result ~50% of the time (measured: 5063/10000).
// The aggregator guards against this with TWO drains: a priority drain
// (non-blocking select at the top of each loop iteration) and a final drain
// (non-blocking loop inside the ctx.Done() arm). Run 200 iterations to make
// the ~50% failure visible.
//
// What this test actually guarantees: the test goes RED only when BOTH drains
// are removed (replacing the whole loop body with a single fair select). A
// 4-cell revert matrix confirms this:
//
//	priority drain | final drain | result
//	---------------+-------------+--------
//	present        | present     | pass
//	removed        | present     | pass (200/200 — final drain catches the buffered result)
//	present        | removed     | pass (priority drain catches it before ctx.Done())
//	removed        | removed     | fail (~50% of iterations drop the result)
//
// The test guards the COMBINED drain, not the priority drain in isolation.
//
// Why no isolating test for the priority drain alone exists: when the final
// drain is present, the priority drain has no independently observable effect.
// The final drain is a perfect functional backstop — it catches any buffered
// result that the priority drain would have caught, via a non-blocking loop
// inside the ctx.Done() arm. Whether the result is taken by the priority drain
// (loop continues normally, partial=false) or by the final drain (markUnreported
// runs but finds nothing unreported, break loop, partial=false), the observable
// output is identical: the same results merged, the same sources classified,
// the same partial flag. The only difference is internal control flow
// (markUnreported runs vs doesn't), which is not externally observable. An
// isolating test would require a seam to detect which code path executed
// (e.g. a hook or counter), which means modifying the aggregator's internal
// structure — out of scope for this task ("do not redesign the aggregator").
// The priority drain's value is defense-in-depth: if the final drain is
// removed in a future refactor, the priority drain still protects.
func TestAggregateSourceResults_PriorityDrain_BufferedResultNotDroppedOnCancellation(t *testing.T) {
	for i := 0; i < 200; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		ch := make(chan sourceResult, 2)
		srcs := []connectors.Source{testSlowSource{sleep: time.Hour}}
		dispatched := map[string]bool{"test-slow": true}

		// Buffer a result, THEN cancel — the result is already in the channel.
		ch <- sourceResult{name: "test-slow", results: make([]engine.SearxngResult, 1), err: nil}
		cancel()

		merged, _, _, sources, partial := aggregateSourceResults(ctx, srcs, false, ch, 1, dispatched)

		if len(merged) != 1 {
			t.Fatalf("iter %d: merged = %d, want 1 (buffered result must not be dropped)", i, len(merged))
		}
		if len(sources) != 1 || sources[0].Outcome != engine.SourceOutcomeOK {
			t.Fatalf("iter %d: sources = %+v, want one ok (buffered result must be classified, not marked failed)", i, sources)
		}
		if partial {
			t.Errorf("iter %d: partial = true, want false (the one expected goroutine reported)", i)
		}
		cancel()
	}
}

// TestAggregateSourceResults_NeverDispatched_MarkedNotDispatched is the
// BLOCKER 4 regression test for the never-dispatched marking. A source that
// was never dispatched (spawn loop cancelled at the semaphore before it could
// run) must be marked "not_dispatched", not "failed" — it never ran, so
// "failed" (ran, errored) would be a lie. And it must NOT be "skipped" either,
// because "skipped" means "ran but declined (missing API key)" — a different
// cause with a different operator action.
//
// Revert-red: if markUnreported stops differentiating dispatched vs
// never-dispatched and marks both as "failed", the outcome assertion fails.
// If it collapses never-dispatched back into "skipped", the outcome assertion
// also fails.
func TestAggregateSourceResults_NeverDispatched_MarkedNotDispatched(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan sourceResult, 1)
	srcs := []connectors.Source{testSlowSource{sleep: time.Hour}}
	dispatched := map[string]bool{} // test-slow was never dispatched

	cancel()

	done := make(chan struct{})
	var sources []engine.SourceStatus
	go func() {
		defer close(done)
		_, _, _, sources, _ = aggregateSourceResults(ctx, srcs, false, ch, 1, dispatched)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("hung")
	}

	if len(sources) != 1 {
		t.Fatalf("sources = %d, want 1", len(sources))
	}
	if sources[0].Outcome != engine.SourceOutcomeNotDispatched {
		t.Errorf("outcome = %q, want %q (never dispatched → not_dispatched, not failed or skipped)",
			sources[0].Outcome, engine.SourceOutcomeNotDispatched)
	}
	if sources[0].Reason == "" {
		t.Error("not_dispatched source must carry a 'not dispatched' reason")
	}
}

// TestBuildUnprocessedSummary_NeverDispatched_DistinguishedFromNoKey: a source
// that was never dispatched (deadline arrived before a concurrency slot) must
// appear in the summary under the "never dispatched" label, NOT under the
// "skipped (set API key)" label. These are different causes with different
// operator actions — conflating them sends the operator after the wrong fix.
//
// Revert-red: if buildUnprocessedSummary lumps not_dispatched and skipped into
// one "did not finish" list (the pre-fix code), the summary contains neither
// "never dispatched" nor "API key" and both assertions fail.
func TestBuildUnprocessedSummary_NeverDispatched_DistinguishedFromNoKey(t *testing.T) {
	sources := []engine.SourceStatus{
		{Name: "indeed", Outcome: engine.SourceOutcomeNotDispatched, Reason: "not dispatched: search deadline reached before concurrency slot acquired"},
	}
	// rawCount=0 is unreachable in production (the handler's len(merged)==0
	// branch diverts to buildZeroResultsSummary before buildUnprocessedSummary
	// can run), but the assertion targets the grouping logic, which is
	// rawCount-independent, so the value is irrelevant to what this test proves.
	summary := buildUnprocessedSummary(sources, 0)
	if !contains(summary, "never dispatched") {
		t.Errorf("summary must label never-dispatched sources distinctly; got: %s", summary)
	}
	if contains(summary, "API key") {
		t.Errorf("summary must not confuse never-dispatched with missing-API-key; got: %s", summary)
	}
}

// TestBuildUnprocessedSummary_NoKey_DistinguishedFromNeverDispatched: a source
// that ran but declined (missing API key) must appear under the "skipped (set
// API key)" label, NOT under "never dispatched". The source WAS dispatched and
// DID run — it returned immediately for want of a credential. The operator
// action is "set INDEED_API_KEY", not "raise the timeout".
//
// Revert-red: if buildUnprocessedSummary lumps skipped and not_dispatched into
// one "did not finish" list (the pre-fix code), the summary contains neither
// "API key" nor "never dispatched" and both assertions fail.
func TestBuildUnprocessedSummary_NoKey_DistinguishedFromNeverDispatched(t *testing.T) {
	sources := []engine.SourceStatus{
		{Name: "indeed", Outcome: engine.SourceOutcomeSkipped, Reason: "no API key: indeed key not configured"},
	}
	// rawCount=0 is unreachable in production (see note in the test above);
	// the assertion targets the grouping logic, which is rawCount-independent.
	summary := buildUnprocessedSummary(sources, 0)
	if !contains(summary, "API key") {
		t.Errorf("summary must label missing-API-key sources distinctly; got: %s", summary)
	}
	if contains(summary, "never dispatched") {
		t.Errorf("summary must not confuse missing-API-key with never-dispatched; got: %s", summary)
	}
}

// TestBuildZeroResultsSummary_NotDispatched_NamesSourceWithOutcomeAndReason:
// the zero-results branch (len(merged)==0) is the path the original OOM incident
// actually took — every source was still waiting for a concurrency slot when the
// deadline fired, so merged was empty and buildZeroResultsSummary ran, NOT
// buildUnprocessedSummary. A not_dispatched source must appear in that summary
// with BOTH its outcome and its reason, so a future refactor of the formatter
// cannot silently drop the outcome/reason distinction and collapse it back to
// the generic "No results found."
//
// Revert-red: if buildZeroResultsSummary stops surfacing not_dispatched sources
// (or drops the reason), the outcome/reason assertions fail.
func TestBuildZeroResultsSummary_NotDispatched_NamesSourceWithOutcomeAndReason(t *testing.T) {
	sources := []engine.SourceStatus{
		{Name: "indeed", Outcome: engine.SourceOutcomeNotDispatched, Reason: "not dispatched: search deadline reached before concurrency slot acquired"},
	}
	summary := buildZeroResultsSummary(sources)
	if !contains(summary, "indeed") {
		t.Fatalf("summary must name the not_dispatched source, not the generic 'No results found.'; got: %s", summary)
	}
	if !contains(summary, engine.SourceOutcomeNotDispatched) {
		t.Errorf("summary must carry the outcome %q; got: %s", engine.SourceOutcomeNotDispatched, summary)
	}
	if !contains(summary, "not dispatched") {
		t.Errorf("summary must carry the reason; got: %s", summary)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// ensure connectors import is used (testSlowSource returns connectors.Source).
var _ connectors.Source = testSlowSource{}

// ensure errors import is used.
var _ = errors.Is

// --- BLOCKER A: delivery chain tests for structured precedence ---
//
// These tests exercise the FULL wiring (runSource → sourceResult.structured →
// aggregateSourceResults → structuredJobs return), NOT just the mappers in
// isolation. The prior test suite called mappers directly, so making the
// StructuredFetcher branch unreachable or deleting the allListings append
// left all tests green. These tests close that gap.

// testStructuredSource is a connectors.Source that also implements
// connectors.StructuredFetcher. It returns a fixed pair of results + listings
// so the delivery chain can be exercised without a live HTTP server.
type testStructuredSource struct {
	results  []engine.SearxngResult
	listings []engine.JobListing
}

func (testStructuredSource) Name() string                        { return "test-structured" }
func (testStructuredSource) Capabilities() connectors.Capability { return 0 }
func (testStructuredSource) Groups() []string                    { return []string{"all"} }
func (testStructuredSource) SiteScope() string                   { return "" }
func (s testStructuredSource) Fetch(_ context.Context, _ connectors.Query) ([]engine.SearxngResult, error) {
	return s.results, nil
}
func (s testStructuredSource) FetchStructured(_ context.Context, _ connectors.Query) ([]engine.SearxngResult, []engine.JobListing, error) {
	return s.results, s.listings, nil
}

// TestRunSource_StructuredFetcher_PopulatesStructuredField verifies that
// runSource, when the source implements connectors.StructuredFetcher, takes
// the FetchStructured branch and populates sourceResult.structured with the
// structured listings. This is the seam that feeds aggregateSourceResults.
//
// Mutation: make the StructuredFetcher branch unreachable (delete the
// type-assertion at tool_job_search.go:523) → structured stays nil → RED.
func TestRunSource_StructuredFetcher_PopulatesStructuredField(t *testing.T) {
	src := testStructuredSource{
		results:  []engine.SearxngResult{{URL: "https://jobs.lever.co/testco/abc", Title: "Eng"}},
		listings: []engine.JobListing{{URL: "https://jobs.lever.co/testco/abc", Title: "Eng", Company: "testco", Source: "lever"}},
	}
	ch := make(chan sourceResult, 1)
	runSource(context.Background(), src, connectors.Query{}, ch)
	r := <-ch
	if r.name != "test-structured" {
		t.Errorf("name = %q, want test-structured", r.name)
	}
	if len(r.results) != 1 {
		t.Errorf("results = %d, want 1", len(r.results))
	}
	if len(r.structured) != 1 {
		t.Fatalf("structured = %d, want 1 (StructuredFetcher branch must populate structured field)", len(r.structured))
	}
	if r.structured[0].Company != "testco" {
		t.Errorf("structured[0].Company = %q, want testco", r.structured[0].Company)
	}
}

// TestAggregateSourceResults_StructuredData_ReturnsStructuredListings verifies
// that aggregateSourceResults threads the structured listings from
// sourceResult.structured into its structuredJobs return value. This is the
// seam that feeds buildHealthySelection in runJobSearch.
//
// Mutation: delete the `allListings = append(...)` line in aggregateSourceResults
// → structuredJobs stays nil → RED.
func TestAggregateSourceResults_StructuredData_ReturnsStructuredListings(t *testing.T) {
	ctx := context.Background()
	ch := make(chan sourceResult, 1)
	srcs := []connectors.Source{testStructuredSource{}}
	dispatched := map[string]bool{"test-structured": true}
	ch <- sourceResult{
		name:       "test-structured",
		results:    []engine.SearxngResult{{URL: "https://jobs.lever.co/testco/abc", Title: "Eng"}},
		structured: []engine.JobListing{{URL: "https://jobs.lever.co/testco/abc", Title: "Eng", Company: "testco", Source: "lever"}},
		err:        nil,
	}

	merged, _, structuredJobs, sources, partial := aggregateSourceResults(ctx, srcs, false, ch, 1, dispatched)
	if len(merged) != 1 {
		t.Errorf("merged = %d, want 1", len(merged))
	}
	if len(structuredJobs) != 1 {
		t.Fatalf("structuredJobs = %d, want 1 (aggregateSourceResults must thread structured listings through)", len(structuredJobs))
	}
	if structuredJobs[0].Company != "testco" {
		t.Errorf("structuredJobs[0].Company = %q, want testco", structuredJobs[0].Company)
	}
	if len(sources) != 1 || sources[0].Outcome != engine.SourceOutcomeOK {
		t.Errorf("sources = %+v, want one ok", sources)
	}
	if partial {
		t.Error("partial = true, want false")
	}
}

// TestRunSource_NonStructuredSource_LeavesStructuredNil verifies that a source
// that does NOT implement StructuredFetcher (the generic-searxng / LinkedIn
// path) leaves sourceResult.structured nil. This is the negative half: it
// guards that the StructuredFetcher branch is NOT taken for non-structured
// sources, so structured data is not fabricated.
func TestRunSource_NonStructuredSource_LeavesStructuredNil(t *testing.T) {
	src := testSlowSource{sleep: 0}
	ch := make(chan sourceResult, 1)
	runSource(context.Background(), src, connectors.Query{}, ch)
	r := <-ch
	if r.structured != nil {
		t.Errorf("structured = %v, want nil (non-StructuredFetcher source must not fabricate structured data)", r.structured)
	}
}

// TestRunJobSearch_StructuredPrecedence_SalaryWinsOverLLMNil drives runJobSearch
// end-to-end through the LLM post-processing path (the path the existing handler
// tests avoid via offset-beyond-total / zero-results / B1-gate early returns).
// A structured ATS listing carrying SalaryMin=160000 is registered via
// withTestRegistry, and the summarizeJobResults seam is stubbed to return an LLM
// record for the SAME URL with Salary "not specified" and SalaryMin nil. The
// output must carry 160000 — proving the structured-LLM join is wired
// between the LLM output and the structured map inside runJobSearch. The join
// lives in jobs.StructuredMatcher (called from buildHealthySelection); the
// field fill is jobs.FillStructuredFromLLM.
//
// Acceptance is the mutation, not a passing test: in buildHealthySelection
// (tool_job_search.go), amputate the structured-match arm — delete the
// `if s, ok := m.Match(j); ok { jobs.FillStructuredFromLLM(&s, j); ...; continue }`
// block so the LLM listing is always emitted unchanged → SalaryMin stays nil
// → RED. Revert → green.
//
// No t.Parallel(): this test swaps the package-level summarizeJobResults var.
// A parallel test in the same package could observe the stubbed value and
// route through the fake summarizer unintentionally. The existing handler tests
// in this package are likewise non-parallel, so the seam is safe.
func TestRunJobSearch_StructuredPrecedence_SalaryWinsOverLLMNil(t *testing.T) {
	const jobURL = "https://jobs.lever.co/testco/abc123"

	minSalary := 160000
	maxSalary := 220000
	src := testStructuredSource{
		results: []engine.SearxngResult{{
			Title:   "Senior Backend Engineer",
			URL:     jobURL,
			Content: "** Senior Backend Engineer at TestCo (markdown marker keeps the content inline, no network fetch)",
		}},
		listings: []engine.JobListing{{
			URL:            jobURL,
			Title:          "Senior Backend Engineer",
			Company:        "TestCo",
			Source:         "lever",
			SalaryMin:      &minSalary,
			SalaryMax:      &maxSalary,
			SalaryCurrency: "USD",
		}},
	}
	withTestRegistry(t, src)

	// Seam: stub the LLM summarizer to return an LLM record for the SAME URL
	// with no salary. Cleanup restores the real implementation; the assertion
	// cleanup (registered FIRST so it runs LAST under LIFO) verifies restoration
	// via reflect pointer identity (func values cannot be compared with ==/!=
	// except against nil).
	orig := summarizeJobResults
	t.Cleanup(func() {
		got := reflect.ValueOf(summarizeJobResults).Pointer()
		want := reflect.ValueOf(orig).Pointer()
		if got != want {
			t.Errorf("summarizeJobResults not restored after t.Cleanup: got %#x, want %#x", got, want)
		}
	})
	t.Cleanup(func() { summarizeJobResults = orig })
	summarizeJobResults = func(_ context.Context, query, _ string, _ int, _ []engine.SearxngResult, _ map[string]string) (*engine.JobSearchOutput, error) {
		return &engine.JobSearchOutput{
			Query:   query,
			Summary: "1 result",
			Jobs: []engine.JobListing{{
				Title:   "Senior Backend Engineer",
				Company: "TestCo",
				URL:     jobURL,
				Salary:  "not specified", // LLM failed to extract salary
				// SalaryMin/Max left nil — the case structured precedence must fix.
			}},
		}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	input := engine.JobSearchInput{
		Query:    "structured-precedence-salary-unique-4f1c",
		Platform: "all",
	}

	_, out, err := runJobSearch(ctx, nil, input)
	if err != nil {
		t.Fatalf("runJobSearch returned error: %v", err)
	}
	if len(out.Jobs) != 1 {
		t.Fatalf("len(out.Jobs) = %d, want 1", len(out.Jobs))
	}
	if out.Jobs[0].URL != jobURL {
		t.Fatalf("out.Jobs[0].URL = %q, want %q", out.Jobs[0].URL, jobURL)
	}
	if out.Jobs[0].SalaryMin == nil {
		t.Fatalf("out.Jobs[0].SalaryMin = nil, want 160000 (structured precedence must override the LLM's nil salary)")
	}
	if *out.Jobs[0].SalaryMin != minSalary {
		t.Errorf("out.Jobs[0].SalaryMin = %d, want %d (structured value must win over LLM nil)", *out.Jobs[0].SalaryMin, minSalary)
	}
	if out.Jobs[0].SalaryMax == nil || *out.Jobs[0].SalaryMax != maxSalary {
		t.Errorf("out.Jobs[0].SalaryMax = %v, want %d", out.Jobs[0].SalaryMax, maxSalary)
	}
	if out.Jobs[0].SalaryCurrency != "USD" {
		t.Errorf("out.Jobs[0].SalaryCurrency = %q, want USD", out.Jobs[0].SalaryCurrency)
	}
}

// testStructuredSourceWithError is a connectors.Source + StructuredFetcher that
// returns BOTH partial results AND an error from FetchStructured — the
// partially-successful source case (tool_job_search.go:532-541).
type testStructuredSourceWithError struct {
	results  []engine.SearxngResult
	listings []engine.JobListing
	err      error
}

func (testStructuredSourceWithError) Name() string                        { return "test-structured-err" }
func (testStructuredSourceWithError) Capabilities() connectors.Capability { return 0 }
func (testStructuredSourceWithError) Groups() []string                    { return []string{"all"} }
func (testStructuredSourceWithError) SiteScope() string                   { return "" }
func (s testStructuredSourceWithError) Fetch(_ context.Context, _ connectors.Query) ([]engine.SearxngResult, error) {
	return s.results, s.err
}
func (s testStructuredSourceWithError) FetchStructured(_ context.Context, _ connectors.Query) ([]engine.SearxngResult, []engine.JobListing, error) {
	return s.results, s.listings, s.err
}

// TestRunSource_StructuredFetcher_ErrorForwardsResults verifies that when a
// StructuredFetcher returns BOTH partial results AND an error, runSource
// forwards BOTH the results and the error on the channel. A future `return`
// on error would silently drop a partially-successful source's results.
//
// Mutation: add `if err != nil { ch <- sourceResult{name: src.Name(), err: err}; return }`
// before the results are forwarded → r.results becomes nil → RED.
func TestRunSource_StructuredFetcher_ErrorForwardsResults(t *testing.T) {
	src := testStructuredSourceWithError{
		results:  []engine.SearxngResult{{URL: "https://jobs.lever.co/testco/abc", Title: "Partial"}},
		listings: []engine.JobListing{{URL: "https://jobs.lever.co/testco/abc", Title: "Partial", Source: "lever"}},
		err:      errors.New("partial failure: upstream timeout"),
	}
	ch := make(chan sourceResult, 1)
	runSource(context.Background(), src, connectors.Query{}, ch)
	r := <-ch
	if r.err == nil {
		t.Fatalf("err = nil, want the partial-failure error (runSource must forward errors)")
	}
	if len(r.results) != 1 {
		t.Errorf("results = %d, want 1 (partial results must be forwarded alongside the error)", len(r.results))
	}
	if len(r.structured) != 1 {
		t.Errorf("structured = %d, want 1 (partial structured listings must be forwarded alongside the error)", len(r.structured))
	}
}

// TestRunJobSearch_UnparseableResult_NotCached is the F1-cache falsification
// test at the runJobSearch level. It swaps the summarizeJobResults seam to
// return an Unparseable=true output and asserts the cache is NOT populated
// after runJobSearch returns. The cache-skip guard (`if !jobOut.Unparseable`)
// in tool_job_search.go is the production branch under test.
//
// Mutation: restore the unconditional `engine.CacheStoreJSON(...)` call (drop
// the `if !jobOut.Unparseable` guard) → the cache load succeeds → RED.
func TestRunJobSearch_UnparseableResult_NotCached(t *testing.T) {
	// Use a source that returns results so runJobSearch reaches the
	// summarizeJobResults call (the empty-source path returns early before
	// the LLM step, never hitting the cache-skip guard).
	src := testResultSource{results: []engine.SearxngResult{
		{URL: "http://example.com/job-1", Title: "Go Dev", Content: "**Source:** test"},
	}}
	withTestRegistry(t, src)

	orig := summarizeJobResults
	t.Cleanup(func() { summarizeJobResults = orig })
	summarizeJobResults = func(_ context.Context, query, _ string, _ int, _ []engine.SearxngResult, _ map[string]string) (*engine.JobSearchOutput, error) {
		return &engine.JobSearchOutput{
			Query:       query,
			Summary:     "LLM response was incomplete/unparseable; 0 source(s) collected.",
			Unparseable: true,
		}, nil
	}

	// InitCache with nil redis → L1-only in-memory cache.
	engine.InitCache("", engine.CacheTTL, 64, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	input := engine.JobSearchInput{
		Query:    "unparseable-cache-skip-unique-7e3a",
		Platform: "all",
	}

	_, out, err := runJobSearch(ctx, nil, input)
	if err != nil {
		t.Fatalf("runJobSearch returned error: %v", err)
	}
	if !out.Unparseable {
		t.Fatalf("test setup: runJobSearch output must have Unparseable=true; got false")
	}

	// Rebuild the SAME cache key runJobSearch used and assert it is NOT cached.
	cacheKey := engine.CacheKey("job_search", input.Query, input.Location, input.Experience,
		input.JobType, input.Remote, input.TimeRange, input.Platform,
		fmt.Sprintf("limit_%d_offset_%d", input.Limit, input.Offset))
	if cached, ok := engine.CacheLoadJSON[engine.JobSearchOutput](ctx, cacheKey); ok {
		t.Errorf("F1-cache FAIL: unparseable result must NOT be cached; got a hit: %+v", cached)
	}
}
