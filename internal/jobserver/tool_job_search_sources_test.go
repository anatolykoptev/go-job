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

	cancel() // cancel BEFORE calling so the ctx.Done() arm fires immediately

	done := make(chan struct{})
	var merged []engine.SearxngResult
	var sources []engine.SourceStatus
	go func() {
		defer close(done)
		merged, _, sources = aggregateSourceResults(ctx, srcs, false, ch, 1)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("aggregateSourceResults hung on cancelled context — ctx.Done() select missing (regression)")
	}

	if len(merged) != 0 {
		t.Errorf("merged = %d, want 0 (no source reported)", len(merged))
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
	ch <- sourceResult{name: "test-slow", results: make([]engine.SearxngResult, 2), err: nil}

	merged, _, sources := aggregateSourceResults(ctx, srcs, false, ch, 1)
	if len(merged) != 2 {
		t.Fatalf("merged = %d, want 2", len(merged))
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
	cancel()

	done := make(chan struct{})
	var sources []engine.SourceStatus
	go func() {
		defer close(done)
		_, _, sources = aggregateSourceResults(ctx, srcs, true, ch, 2)
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
