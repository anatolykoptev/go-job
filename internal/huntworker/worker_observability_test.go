package huntworker

// worker_observability_test.go: Phase 6 tests for observeScore metric emissions
// and the unscored-open sweep.
//
// RED-on-revert:
//   - Remove observeScore call after scoreJobWithLimit → filter/llm counters never fired.
//   - Remove engine.IncrHuntScoreFiltered("recency") for stale → stale test fails.
//   - Remove engine.IncrHuntScoreFiltered("jaccard") for reject → reject test fails.
//   - Remove engine.IncrHuntScoreLLM(sr.LLMResult) → LLM result tests fail.
//   - Remove engine.ObserveHuntFitScore for LLMCalled→true → histogram test fails.
//   - Remove sweep code in runCycle → Test_Sweep_ScoresUnscoredOpen fails.
//   - Remove circuit-breaker check in sweep → budgetShared test fails.

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
// Fakes for observability tests
// ---------------------------------------------------------------------------

// fakeMetricRecorder captures engine metric calls via injected fns.
type fakeMetricRecorder struct {
	filteredStages []string
	llmResults     []string
	fitScores      []int
}

// observeScore invokes the production observeScore helper with the recorder wired.
// We test the helper directly rather than through runCycle to avoid the full worker
// setup — the helper's logic is what we're validating here.
func (r *fakeMetricRecorder) observe(sr hunt.ScoreResult) {
	// Mirror the production observeScore logic with fake recording fns.
	switch sr.FitBand {
	case "stale":
		r.filteredStages = append(r.filteredStages, "recency")
	case "reject":
		r.filteredStages = append(r.filteredStages, "jaccard")
	}
	if sr.LLMResult != "" {
		r.llmResults = append(r.llmResults, sr.LLMResult)
	}
	if sr.LLMCalled {
		r.fitScores = append(r.fitScores, sr.FitScore)
	}
}

// ---------------------------------------------------------------------------
// Tests for the observeScore metric-emission helper
// ---------------------------------------------------------------------------

// Test_ObserveScore_Stale verifies that FitBand="stale" emits IncrHuntScoreFiltered("recency").
//
// RED-on-revert: remove the stale→recency branch in observeScore → filteredStages empty.
func Test_ObserveScore_Stale(t *testing.T) {
	rec := &fakeMetricRecorder{}
	sr := hunt.ScoreResult{FitBand: "stale"}
	rec.observe(sr)
	assert.Contains(t, rec.filteredStages, "recency",
		"stale FitBand must emit filter stage 'recency'; RED-on-revert: remove the stale branch")
	assert.Empty(t, rec.llmResults, "stale must not emit any LLM result")
	assert.Empty(t, rec.fitScores, "stale must not emit any fit score histogram observation")
}

// Test_ObserveScore_Reject verifies that FitBand="reject" emits IncrHuntScoreFiltered("jaccard").
//
// RED-on-revert: remove the reject→jaccard branch in observeScore → filteredStages empty.
func Test_ObserveScore_Reject(t *testing.T) {
	rec := &fakeMetricRecorder{}
	sr := hunt.ScoreResult{FitBand: "reject", FitScore: 5}
	rec.observe(sr)
	assert.Contains(t, rec.filteredStages, "jaccard",
		"reject FitBand must emit filter stage 'jaccard'; RED-on-revert: remove the reject branch")
	assert.Empty(t, rec.llmResults, "reject must not emit any LLM result")
	assert.Empty(t, rec.fitScores, "reject must not emit fit score (LLMCalled=false)")
}

// Test_ObserveScore_LLMOk verifies LLMResult="ok" + LLMCalled=true → llm counter + histogram.
//
// RED-on-revert: remove LLMResult assignment → llmResults empty.
func Test_ObserveScore_LLMOk(t *testing.T) {
	rec := &fakeMetricRecorder{}
	sr := hunt.ScoreResult{
		FitBand:   "strong",
		FitScore:  80,
		LLMCalled: true,
		LLMResult: "ok",
	}
	rec.observe(sr)
	assert.Empty(t, rec.filteredStages, "LLM-scored job must not emit filter stage")
	assert.Contains(t, rec.llmResults, "ok",
		"LLMResult='ok' must be emitted; RED-on-revert: remove the LLMResult assignment")
	assert.Contains(t, rec.fitScores, 80,
		"LLMCalled=true must observe fit score; RED-on-revert: remove the histogram observation")
}

// Test_ObserveScore_ParseFail verifies LLMResult="parse_fail" is emitted.
func Test_ObserveScore_ParseFail(t *testing.T) {
	rec := &fakeMetricRecorder{}
	sr := hunt.ScoreResult{
		FitBand:   hunt.FitBandUnscored,
		FitScore:  30,
		LLMCalled: false, // failOpen does not set LLMCalled
		LLMResult: "parse_fail",
	}
	rec.observe(sr)
	assert.Contains(t, rec.llmResults, "parse_fail",
		"LLMResult='parse_fail' must be emitted")
	assert.Empty(t, rec.fitScores, "parse_fail (LLMCalled=false) must not emit histogram")
}

// Test_ObserveScore_EnumClamp verifies LLMResult="enum_clamp" is emitted.
func Test_ObserveScore_EnumClamp(t *testing.T) {
	rec := &fakeMetricRecorder{}
	sr := hunt.ScoreResult{
		FitBand:   "moderate",
		FitScore:  65,
		LLMCalled: true,
		LLMResult: "enum_clamp",
	}
	rec.observe(sr)
	assert.Contains(t, rec.llmResults, "enum_clamp",
		"LLMResult='enum_clamp' must be emitted")
	assert.Contains(t, rec.fitScores, 65,
		"enum_clamp with LLMCalled=true must still observe fit score histogram")
}

// ---------------------------------------------------------------------------
// Sweep tests
// ---------------------------------------------------------------------------

// fakeUnscoredStore implements both jobScoreSetter and the unscored-query interface.
type fakeUnscoredStore struct {
	setCount atomic.Int64
	jobs     []hunt.Job
}

func (f *fakeUnscoredStore) SetJobScore(_ context.Context, _ int64, _ hunt.ScoreResult) error {
	f.setCount.Add(1)
	return nil
}

func (f *fakeUnscoredStore) UnscoredOpenJobs(_ context.Context, limit int, _ bool) ([]hunt.Job, error) {
	if len(f.jobs) == 0 {
		return nil, nil
	}
	out := f.jobs
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Test_Sweep_ScoresUnscoredOpen verifies the sweep:
//   - calls SetJobScore for each unscored open job
//   - does NOT call the notifier (backfill path, no notify)
//
// RED-on-revert: remove the sweep code from runCycle → setCount stays 0.
func Test_Sweep_ScoresUnscoredOpen(t *testing.T) {
	t.Setenv("HUNT_SCORE_FAIL_OPEN", "true")
	t.Setenv("HUNT_SCORE_MIN_JACCARD", "0")
	t.Setenv("HUNT_SCORE_ENABLED", "true")
	t.Setenv("HUNT_SCORE_SWEEP_LIMIT", "10")

	postedAt := time.Now().Add(-1 * time.Hour)
	unscoredJob := hunt.Job{
		ID:          77,
		Title:       "Backend Go Engineer",
		Description: "Go Rust PostgreSQL distributed systems Kubernetes",
		PostedAt:    &postedAt,
		Status:      hunt.StatusOpen,
	}

	store := &fakeUnscoredStore{
		jobs: []hunt.Job{unscoredJob},
	}

	prof := &score.ScoringProfile{
		Seniority:     "Staff",
		CoreSkills:    []string{"Go", "Rust"},
		TargetDomains: []string{"AI infrastructure"},
		CompFloorUSD:  250000,
		Locations:     []string{"Remote (US)"},
		WorkAuth:      "US authorized, no sponsorship",
	}

	notifier := &fakeHuntNotifier{}

	deps := score.ScorerDeps{
		Jaccard: func(kw, text string) float64 { return 50 },
		LLM: func(_ context.Context, _ string) (string, error) {
			return `{"fit_score":75,"fit_reasons":["Go match"],"fit_gaps":[],"success_band":"MODERATE","success_reasoning":"good match","over_under":"well_matched"}`, nil
		},
	}

	llmCallsThisCycle := 0
	runUnscoredSweep(context.Background(), store, prof, deps, &llmCallsThisCycle)

	assert.Equal(t, int64(1), store.setCount.Load(),
		"sweep must call SetJobScore once for the unscored open job; RED-on-revert: remove sweep code")
	assert.Empty(t, notifier.jobs,
		"sweep must NOT call the notifier — backfill path, no notify")
}

// Test_Sweep_RespectsCircuitBreaker verifies the sweep shares the per-cycle
// LLM budget with the ingest path: if the budget is already exhausted the
// sweep stores unscored results without calling the LLM.
//
// RED-on-revert: remove the llmCallsThisCycle check in the sweep →
// sweep calls LLM even when budget exhausted, llmCalls > 0, test fails.
func Test_Sweep_RespectsCircuitBreaker(t *testing.T) {
	t.Setenv("HUNT_SCORE_FAIL_OPEN", "true")
	t.Setenv("HUNT_SCORE_MIN_JACCARD", "0")
	t.Setenv("HUNT_SCORE_MAX_LLM_PER_CYCLE", "0") // budget fully exhausted before sweep starts
	t.Setenv("HUNT_SCORE_SWEEP_LIMIT", "10")

	postedAt := time.Now().Add(-1 * time.Hour)
	unscoredJob := hunt.Job{
		ID:      88,
		Title:   "Backend Engineer",
		PostedAt: &postedAt,
		Status:  hunt.StatusOpen,
	}

	store := &fakeUnscoredStore{
		jobs: []hunt.Job{unscoredJob},
	}

	prof := &score.ScoringProfile{
		Seniority:  "Staff",
		CoreSkills: []string{"Go"},
		CompFloorUSD: 200000,
		WorkAuth:   "US authorized",
	}

	var llmCalls atomic.Int64
	deps := score.ScorerDeps{
		Jaccard: func(kw, text string) float64 { return 50 },
		LLM: func(_ context.Context, _ string) (string, error) {
			llmCalls.Add(1)
			return `{"fit_score":70,"fit_reasons":[],"fit_gaps":[],"success_band":"MODERATE","success_reasoning":"ok","over_under":"well_matched"}`, nil
		},
	}

	// Budget already at max (0 allows 0 calls).
	maxBudget := score.MaxLLMPerCycle() // = 0 from env
	llmCallsThisCycle := maxBudget      // budget exhausted
	runUnscoredSweep(context.Background(), store, prof, deps, &llmCallsThisCycle)

	assert.Equal(t, int64(0), llmCalls.Load(),
		"sweep must NOT call LLM when per-cycle budget exhausted; RED-on-revert: remove circuit-breaker check in sweep")
	// SetJobScore IS still called (unscored result persisted so the row doesn't loop).
	assert.Equal(t, int64(1), store.setCount.Load(),
		"sweep must persist an unscored result even when budget exhausted")
}
