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
// arms. With a fair select, a buffered result + cancelled context drops the
// result ~50% of the time (measured: 5063/10000). The priority-drain pattern
// must collect the buffered result BEFORE honouring ctx.Done(). Run 200
// iterations to make the ~50% failure visible.
//
// Revert-red: replace the priority-drain select with a single fair select
// (both case r and case <-ctx.Done() in the same select) and ~50% of
// iterations will fail.
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

// TestAggregateSourceResults_NeverDispatched_MarkedSkipped is the BLOCKER 4
// regression test for the never-dispatched marking. A source that was never
// dispatched (spawn loop cancelled at the semaphore before it could run) must
// be marked "skipped", not "failed" — it never ran, so "failed" (ran, errored)
// would be a lie.
//
// Revert-red: if markUnreported stops differentiating dispatched vs
// never-dispatched and marks both as "failed", the outcome assertion fails.
func TestAggregateSourceResults_NeverDispatched_MarkedSkipped(t *testing.T) {
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
	if sources[0].Outcome != engine.SourceOutcomeSkipped {
		t.Errorf("outcome = %q, want %q (never dispatched → skipped, not failed)",
			sources[0].Outcome, engine.SourceOutcomeSkipped)
	}
	if sources[0].Reason == "" {
		t.Error("skipped source must carry a 'not dispatched' reason")
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
