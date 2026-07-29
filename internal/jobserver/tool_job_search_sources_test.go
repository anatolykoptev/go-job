package jobserver

import (
	"context"
	"errors"
	"fmt"
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
		merged, _, sources, partial = aggregateSourceResults(ctx, srcs, false, ch, 1, dispatched)
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

	merged, _, sources, partial := aggregateSourceResults(ctx, srcs, false, ch, 1, dispatched)
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
		_, _, sources, _ = aggregateSourceResults(ctx, srcs, true, ch, 2, dispatched)
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

		merged, _, sources, partial := aggregateSourceResults(ctx, srcs, false, ch, 1, dispatched)

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
		_, _, sources, _ = aggregateSourceResults(ctx, srcs, false, ch, 1, dispatched)
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
	summary := buildUnprocessedSummary(sources, 0)
	if !contains(summary, "API key") {
		t.Errorf("summary must label missing-API-key sources distinctly; got: %s", summary)
	}
	if contains(summary, "never dispatched") {
		t.Errorf("summary must not confuse missing-API-key with never-dispatched; got: %s", summary)
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
