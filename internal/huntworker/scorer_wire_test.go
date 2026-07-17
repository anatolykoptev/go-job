package huntworker

// scorer_wire_test.go: tests that the scorer is wired correctly into runCycle.
//
// These tests use the internal package to access unexported functions directly.
// The key invariant: Score is called exactly once per OutcomeCreated job and
// the result is persisted via SetJobScore. For OutcomeMerged, Score is NOT called.
//
// RED-on-revert:
//   - Remove the scoring call in runCycle → llmCalls stays 0, test fails.
//   - Remove the SetJobScore call → scoreStore.setCount stays 0, test fails.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/anatolykoptev/go_job/internal/hunt/score"
	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// Fake store for scoring tests
// ---------------------------------------------------------------------------

// fakeScoreStore counts SetJobScore calls.
type fakeScoreStore struct {
	setCount atomic.Int64
}

func (f *fakeScoreStore) SetJobScore(_ context.Context, _ int64, _ hunt.ScoreResult) error {
	f.setCount.Add(1)
	return nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestScoreOnce_OnCreated verifies that the scorer is invoked once for an
// OutcomeCreated job and that the result is persisted.
//
// RED-on-revert: remove the `if outcome == hunt.OutcomeCreated` scoring block
// in runCycle → llmCalls stays 0, setCount stays 0.
func TestScoreOnce_OnCreated(t *testing.T) {
	t.Setenv("HUNT_SCORE_FAIL_OPEN", "true")
	t.Setenv("HUNT_SCORE_MIN_JACCARD", "0") // pass all Jaccard gates

	var llmCalls atomic.Int64
	scoreStore := &fakeScoreStore{}

	postedAt := time.Now().Add(-1 * time.Hour)
	job := hunt.Job{
		ID:          42,
		Title:       "Senior Go Engineer",
		Description: "Go Rust PostgreSQL distributed systems",
		PostedAt:    &postedAt,
	}
	prof := &score.ScoringProfile{
		Seniority:     "Staff",
		CoreSkills:    []string{"Go", "Rust"},
		TargetDomains: []string{"AI infrastructure"},
		CompFloorUSD:  250000,
		Locations:     []string{"Remote (US)"},
		WorkAuth:      "US authorized, no sponsorship",
	}
	deps := score.ScorerDeps{
		Jaccard: func(kw, text string) float64 { return 50 },
		LLM: func(_ context.Context, _ string) (string, error) {
			llmCalls.Add(1)
			return `{"fit_score":80,"fit_reasons":["Go match"],"fit_gaps":[],"success_band":"MODERATE","success_reasoning":"good match","over_under":"well_matched"}`, nil
		},
	}

	scoreJobIfCreated(context.Background(), hunt.OutcomeCreated, job, prof, deps, scoreStore)

	assert.Equal(t, int64(1), llmCalls.Load(), "LLM must be called once for OutcomeCreated")
	assert.Equal(t, int64(1), scoreStore.setCount.Load(), "SetJobScore must be called once for OutcomeCreated")
}

// TestScoreOnce_SkipsOnMerged verifies that OutcomeMerged jobs are not re-scored.
//
// RED-on-revert: remove the `outcome == hunt.OutcomeCreated` guard →
// merged job reaches LLM, llmCalls becomes 1, test fails.
func TestScoreOnce_SkipsOnMerged(t *testing.T) {
	t.Setenv("HUNT_SCORE_FAIL_OPEN", "true")

	var llmCalls atomic.Int64
	scoreStore := &fakeScoreStore{}

	postedAt := time.Now().Add(-1 * time.Hour)
	job := hunt.Job{
		ID:          43,
		Title:       "Senior Go Engineer",
		Description: "Go Rust PostgreSQL",
		PostedAt:    &postedAt,
	}
	prof := &score.ScoringProfile{
		CoreSkills: []string{"Go"},
	}
	deps := score.ScorerDeps{
		Jaccard: func(kw, text string) float64 { return 50 },
		LLM: func(_ context.Context, _ string) (string, error) {
			llmCalls.Add(1)
			return `{"fit_score":80,"fit_reasons":[],"fit_gaps":[],"success_band":"STRONG","success_reasoning":"great","over_under":"well_matched"}`, nil
		},
	}

	scoreJobIfCreated(context.Background(), hunt.OutcomeMerged, job, prof, deps, scoreStore)

	assert.Equal(t, int64(0), llmCalls.Load(), "LLM must NOT be called for OutcomeMerged")
	assert.Equal(t, int64(0), scoreStore.setCount.Load(), "SetJobScore must NOT be called for OutcomeMerged")
}

// TestScoreOnce_NilProfile_Skips verifies that nil profile skips scoring.
func TestScoreOnce_NilProfile_Skips(t *testing.T) {
	var llmCalls atomic.Int64
	scoreStore := &fakeScoreStore{}

	postedAt := time.Now().Add(-1 * time.Hour)
	job := hunt.Job{ID: 44, Title: "Go Engineer", PostedAt: &postedAt}
	deps := score.ScorerDeps{
		Jaccard: func(kw, text string) float64 { return 50 },
		LLM: func(_ context.Context, _ string) (string, error) {
			llmCalls.Add(1)
			return `{}`, nil
		},
	}

	scoreJobIfCreated(context.Background(), hunt.OutcomeCreated, job, nil, deps, scoreStore)

	// nil profile means scoring disabled; scorer returns "unscored" but we still
	// persist the unscored result so the row is marked as "scoring attempted".
	assert.Equal(t, int64(0), llmCalls.Load(), "nil profile must NOT call LLM")
	// SetJobScore IS called even for unscored, to mark scored_at (prevents re-score loops).
	assert.Equal(t, int64(1), scoreStore.setCount.Load(), "SetJobScore must still be called for unscored (marks row)")
}

// TestMaxLLMPerCycle_CircuitBreaker verifies that the circuit breaker stops
// LLM calls once the per-cycle limit is reached.
//
// RED-on-revert: remove the llmCallsThisCycle check → all 5 jobs reach LLM.
func TestMaxLLMPerCycle_CircuitBreaker(t *testing.T) {
	t.Setenv("HUNT_SCORE_MAX_LLM_PER_CYCLE", "2")
	t.Setenv("HUNT_SCORE_MIN_JACCARD", "0")
	t.Setenv("HUNT_SCORE_FAIL_OPEN", "true")

	var llmCalls atomic.Int64
	scoreStore := &fakeScoreStore{}
	postedAt := time.Now().Add(-1 * time.Hour)
	prof := &score.ScoringProfile{CoreSkills: []string{"Go"}}
	deps := score.ScorerDeps{
		Jaccard: func(kw, text string) float64 { return 50 },
		LLM: func(_ context.Context, _ string) (string, error) {
			llmCalls.Add(1)
			return `{"fit_score":80,"fit_reasons":[],"fit_gaps":[],"success_band":"STRONG","success_reasoning":"good","over_under":"well_matched"}`, nil
		},
	}

	var cycleCounter atomic.Int64
	for i := 0; i < 5; i++ {
		job := hunt.Job{
			ID:          int64(100 + i),
			Title:       "Go Engineer",
			Description: "Go systems",
			PostedAt:    &postedAt,
		}
		scoreJobWithLimit(context.Background(), hunt.OutcomeCreated, job, prof, deps, scoreStore, &cycleCounter)
	}

	assert.Equal(t, int64(2), llmCalls.Load(),
		"circuit breaker must stop LLM calls at HUNT_SCORE_MAX_LLM_PER_CYCLE=2")
	// SetJobScore is called only for the 2 LLM-scored jobs. The 3 breaker-tripped
	// jobs are NOT persisted (scored_at stays NULL) so the sweep can retry them
	// in the next cycle — persisting scored_at=NOW() would permanently strand them.
	assert.Equal(t, int64(2), scoreStore.setCount.Load(),
		"SetJobScore must be called only for LLM-scored jobs — breaker-tripped jobs stay in unscored pool")
}

// TestMaxLLMPerCycle_StaleJobsDoNotConsumeCircuitBreakerBudget verifies that
// stale and sub-Jaccard jobs do NOT increment the per-cycle LLM counter. The
// bug was: llmCallsThisCycle incremented unconditionally, so 50 stale jobs
// tripped the breaker while the real LLM budget was never spent.
//
// Setup: limit=2; feed 3 stale + 2 fit jobs in sequence.
// Expected: exactly 2 LLM calls (the 2 fit jobs), breaker does NOT trip after stale jobs.
//
// RED-on-revert: revert counter to unconditional increment → llmCalls==0 (stale
// jobs trip the breaker before any fit job runs), test fails.
func TestMaxLLMPerCycle_StaleJobsDoNotConsumeCircuitBreakerBudget(t *testing.T) {
	t.Setenv("HUNT_SCORE_MAX_LLM_PER_CYCLE", "2")
	t.Setenv("HUNT_SCORE_MIN_JACCARD", "8") // default threshold
	t.Setenv("HUNT_SCORE_FAIL_OPEN", "true")

	var llmCalls atomic.Int64
	scoreStore := &fakeScoreStore{}

	stalePosted := time.Now().Add(-72 * time.Hour) // 3 days old → stale
	freshPosted := time.Now().Add(-1 * time.Hour)  // fresh → passes recency gate

	prof := &score.ScoringProfile{CoreSkills: []string{"Go"}}

	fitDeps := score.ScorerDeps{
		Jaccard: func(kw, text string) float64 { return 50 }, // above threshold
		LLM: func(_ context.Context, _ string) (string, error) {
			llmCalls.Add(1)
			return `{"fit_score":80,"fit_reasons":[],"fit_gaps":[],"success_band":"STRONG","success_reasoning":"good","over_under":"well_matched"}`, nil
		},
	}

	staleDeps := score.ScorerDeps{
		Jaccard: func(kw, text string) float64 { return 50 }, // above threshold but recency gate fires
		LLM:     fitDeps.LLM,
	}

	var cycleCounter atomic.Int64

	// 3 stale jobs — must NOT consume any LLM budget.
	for i := 0; i < 3; i++ {
		job := hunt.Job{
			ID:          int64(200 + i),
			Title:       "Go Engineer",
			Description: "Go systems",
			PostedAt:    &stalePosted,
		}
		scoreJobWithLimit(context.Background(), hunt.OutcomeCreated, job, prof, staleDeps, scoreStore, &cycleCounter)
	}

	// 2 fit jobs — must each call the LLM (budget is not exhausted by stale jobs).
	for i := 0; i < 2; i++ {
		job := hunt.Job{
			ID:          int64(210 + i),
			Title:       "Go Engineer",
			Description: "Go systems",
			PostedAt:    &freshPosted,
		}
		scoreJobWithLimit(context.Background(), hunt.OutcomeCreated, job, prof, fitDeps, scoreStore, &cycleCounter)
	}

	// Stale jobs must not have used any LLM budget.
	assert.Equal(t, int64(2), llmCalls.Load(),
		"stale jobs must not consume LLM budget; expected exactly 2 LLM calls (the fit jobs)")
	// cycleCounter must reflect only real LLM calls: 2.
	assert.Equal(t, int64(2), cycleCounter.Load(),
		"llmCallsThisCycle must count only actual LLM calls, not stale-shortcircuited ones")
}
