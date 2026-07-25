package huntworker

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/anatolykoptev/go_job/internal/hunt/score"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestScoringDegraded_BudgetExhausted_DoesNotSetGauge verifies that budget
// exhaustion (per-cycle LLM cap reached) does NOT set the degraded gauge.
// Budget exhaustion is normal operation — the sweep picks up remaining jobs
// in subsequent cycles. The gauge should only be set for actual degradation
// (llm_error, parse_fail, breaker open).
//
// RED-on-revert: restore the SetHuntScoringDegraded(true) call in the
// budget-exhaustion branch of scoreJobWithLimit → gauge becomes 1, test fails.
func TestScoringDegraded_BudgetExhausted_DoesNotSetGauge(t *testing.T) {
	engine.InitTestRegistry()
	t.Setenv("HUNT_SCORE_MAX_LLM_PER_CYCLE", "1")

	store := &fakeScoreSetter{}
	var llmCalls atomic.Int64
	llmCalls.Store(1) // budget already exhausted (1 >= maxLLM=1)

	job := hunt.Job{ID: 100}
	result := scoreJobWithLimit(context.Background(), hunt.OutcomeCreated, job, nil, score.ScorerDeps{}, store, &llmCalls)

	require.NotNil(t, result, "budget-exhausted result must be returned")
	assert.Equal(t, hunt.FitBandUnscored, result.FitBand, "budget-exhausted job must be unscored")

	got := engine.GetGaugeValue(engine.MetricHuntScoringDegraded)
	assert.Equal(t, float64(0), got,
		"budget exhaustion must NOT set degraded gauge — it is normal operation, not degradation")
}

// TestScoringDegraded_LatchFix_CleanCycleClearsGauge verifies the full
// latch-fix invariant: drive a cycle into the degraded branch (llm_error),
// assert gauge is 1; then simulate a clean cycle start (reset) followed by
// budget exhaustion, and assert the gauge returns to 0. Before the fix the
// budget-exhaustion branch re-latched the gauge to 1 every cycle.
//
// RED-on-revert: restore SetHuntScoringDegraded(true) in the budget branch
// → the post-clean-cycle assertion fails (gauge stays 1).
func TestScoringDegraded_LatchFix_CleanCycleClearsGauge(t *testing.T) {
	engine.InitTestRegistry()
	t.Setenv("HUNT_SCORE_FAIL_OPEN", "true")
	t.Setenv("HUNT_SCORE_MIN_JACCARD", "0")
	t.Setenv("HUNT_SCORE_MAX_LLM_PER_CYCLE", "1")

	store := &fakeScoreSetter{}
	postedAt := time.Now().Add(-1 * time.Hour)
	prof := &score.ScoringProfile{Seniority: "Staff", CoreSkills: []string{"Go"}}
	depsErr := score.ScorerDeps{
		Jaccard: func(_, _ string) float64 { return 50 },
		LLM: func(_ context.Context, _ string) (string, error) {
			return "", assert.AnError // llm_error → degraded
		},
	}

	// Phase 1: drive into degraded via llm_error.
	var llmCalls atomic.Int64
	job1 := hunt.Job{ID: 200, Title: "Go Dev", Description: "Go Rust distributed", PostedAt: &postedAt}
	r1 := scoreJobWithLimit(context.Background(), hunt.OutcomeCreated, job1, prof, depsErr, store, &llmCalls)
	require.NotNil(t, r1)
	assert.Equal(t, "llm_error", r1.LLMResult, "phase 1 must produce llm_error")

	got := engine.GetGaugeValue(engine.MetricHuntScoringDegraded)
	assert.Equal(t, float64(1), got, "gauge must be 1 after llm_error degradation (phase 1)")

	// Phase 2: simulate cycle start (reset to 0) + budget exhaustion (clean cycle).
	engine.SetHuntScoringDegraded(false, "cycle_reset")
	llmCalls.Store(1) // budget exhausted (1 >= maxLLM=1)

	job2 := hunt.Job{ID: 201, Title: "Rust Dev", Description: "Rust Go systems", PostedAt: &postedAt}
	r2 := scoreJobWithLimit(context.Background(), hunt.OutcomeCreated, job2, prof, depsErr, store, &llmCalls)
	require.NotNil(t, r2)
	assert.Equal(t, hunt.FitBandUnscored, r2.FitBand, "budget-exhausted job must be unscored")

	got = engine.GetGaugeValue(engine.MetricHuntScoringDegraded)
	assert.Equal(t, float64(0), got,
		"gauge must return to 0 after clean cycle with budget exhaustion — budget exhaustion is not degradation (phase 2)")
}

// TestScoringDegraded_BudgetExhausted_IncrementsSkippedBudgetCounter verifies
// that jobs landing unscored because the per-cycle LLM cap was reached are
// countable via gojob_hunt_score_llm_total{result="skipped_budget"}.
//
// RED-on-revert: remove the skipped_budget LLMResult from the budget-exhausted
// branch → the counter stays 0, test fails.
func TestScoringDegraded_BudgetExhausted_IncrementsSkippedBudgetCounter(t *testing.T) {
	engine.InitTestRegistry()
	t.Setenv("HUNT_SCORE_MAX_LLM_PER_CYCLE", "1")

	before := engine.GetMetrics()

	store := &fakeScoreSetter{}
	var llmCalls atomic.Int64
	llmCalls.Store(1) // budget exhausted

	job := hunt.Job{ID: 300}
	r := scoreJobWithLimit(context.Background(), hunt.OutcomeCreated, job, nil, score.ScorerDeps{}, store, &llmCalls)
	require.NotNil(t, r)
	assert.Equal(t, "skipped_budget", r.LLMResult, "budget-exhausted job must carry LLMResult=skipped_budget")

	// observeScore is the production path that increments IncrHuntScoreLLM.
	observeScore(*r)

	after := engine.GetMetrics()
	key := engine.MetricHuntScoreLLM + "{result=skipped_budget}"
	delta := after[key] - before[key]
	assert.Equal(t, int64(1), delta,
		"skipped_budget counter must increment by 1 for a budget-exhausted job; got delta=%d", delta)
}

// TestScoringDegraded_ReasonCounter_IncrementsOnLLMError verifies that the
// gojob_hunt_scoring_degraded_total{reason="llm_error"} counter increments
// when the degraded gauge is set due to an LLM error.
//
// RED-on-revert: remove the IncrHuntScoringDegradedReason call from
// SetHuntScoringDegraded → counter stays 0, test fails.
func TestScoringDegraded_ReasonCounter_IncrementsOnLLMError(t *testing.T) {
	engine.InitTestRegistry()
	t.Setenv("HUNT_SCORE_FAIL_OPEN", "true")
	t.Setenv("HUNT_SCORE_MIN_JACCARD", "0")

	before := engine.GetMetrics()

	store := &fakeScoreSetter{}
	postedAt := time.Now().Add(-1 * time.Hour)
	job := hunt.Job{ID: 400, Title: "Go Dev", Description: "Go Rust", PostedAt: &postedAt}
	prof := &score.ScoringProfile{Seniority: "Staff", CoreSkills: []string{"Go"}}
	deps := score.ScorerDeps{
		Jaccard: func(_, _ string) float64 { return 50 },
		LLM: func(_ context.Context, _ string) (string, error) {
			return "", assert.AnError
		},
	}

	_ = scoreJobIfCreated(context.Background(), hunt.OutcomeCreated, job, prof, deps, store)

	after := engine.GetMetrics()
	key := engine.MetricHuntScoringDegradedReason + "{reason=llm_error}"
	delta := after[key] - before[key]
	assert.Equal(t, int64(1), delta,
		"degraded_total{reason=llm_error} must increment by 1 on llm_error degradation; got delta=%d", delta)
}
