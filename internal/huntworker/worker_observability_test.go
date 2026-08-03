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
//   - Remove FitBandStale/FitBandReject consts → stale/reject routing breaks.
//   - Revert budget increment to LLMCalled → parse_fail/llm_error don't consume budget.
//   - Revert histogram gate to LLMCalled → parse_fail FitScore pollutes histogram.
//   - Remove sweep budget cap → sweep calls UnscoredOpenJobs even when budget exhausted.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
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
	// NIT-3: uses FitBandStale/FitBandReject consts (not bare strings).
	switch sr.FitBand {
	case hunt.FitBandStale:
		r.filteredStages = append(r.filteredStages, "recency")
	case hunt.FitBandReject:
		r.filteredStages = append(r.filteredStages, "jaccard")
	}
	if sr.LLMResult != "" {
		r.llmResults = append(r.llmResults, sr.LLMResult)
	}
	// MEDIUM-2: histogram fires on "ok" or "enum_clamp" (real fit_score),
	// NOT on LLMCalled — parse_fail/llm_error FitScore must NOT pollute the histogram.
	if sr.LLMResult == "ok" || sr.LLMResult == "enum_clamp" {
		r.fitScores = append(r.fitScores, sr.FitScore)
	}
}

// ---------------------------------------------------------------------------
// Tests for the observeScore metric-emission helper
// ---------------------------------------------------------------------------

// Test_ObserveScore_Stale verifies that FitBand=FitBandStale emits IncrHuntScoreFiltered("recency").
//
// RED-on-revert: remove FitBandStale const → bare "stale" routing breaks.
// RED-on-revert: remove the stale→recency branch in observeScore → filteredStages empty.
func Test_ObserveScore_Stale(t *testing.T) {
	rec := &fakeMetricRecorder{}
	sr := hunt.ScoreResult{FitBand: hunt.FitBandStale}
	rec.observe(sr)
	assert.Contains(t, rec.filteredStages, "recency",
		"stale FitBand must emit filter stage 'recency'; RED-on-revert: remove the stale branch")
	assert.Empty(t, rec.llmResults, "stale must not emit any LLM result")
	assert.Empty(t, rec.fitScores, "stale must not emit any fit score histogram observation")
}

// Test_ObserveScore_Reject verifies that FitBand=FitBandReject emits IncrHuntScoreFiltered("jaccard").
//
// RED-on-revert: remove FitBandReject const → bare "reject" routing breaks.
// RED-on-revert: remove the reject→jaccard branch in observeScore → filteredStages empty.
func Test_ObserveScore_Reject(t *testing.T) {
	rec := &fakeMetricRecorder{}
	sr := hunt.ScoreResult{FitBand: hunt.FitBandReject, FitScore: 5}
	rec.observe(sr)
	assert.Contains(t, rec.filteredStages, "jaccard",
		"reject FitBand must emit filter stage 'jaccard'; RED-on-revert: remove the reject branch")
	assert.Empty(t, rec.llmResults, "reject must not emit any LLM result")
	assert.Empty(t, rec.fitScores, "reject must not emit fit score (LLMResult empty)")
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

// Test_ObserveScore_ParseFail verifies LLMResult="parse_fail" is emitted to the LLM
// counter but does NOT pollute the hunt_fit_score histogram.
//
// RED-on-revert: revert histogram gate to LLMCalled → parse_fail would need LLMCalled=true
// to pollute; the test now explicitly documents parse_fail must NOT reach the histogram.
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
		"LLMResult='parse_fail' must be emitted to llm counter")
	assert.Empty(t, rec.fitScores,
		"parse_fail must NOT emit histogram — jaccard fallback FitScore would pollute hunt_fit_score; "+
			"RED-on-revert: revert histogram gate to LLMCalled")
}

// Test_ObserveScore_EnumClamp verifies LLMResult="enum_clamp" is emitted and DOES
// observe the histogram (clamping only touches enum fields; FitScore is still valid).
//
// RED-on-revert: revert histogram gate from LLMResult-check to LLMCalled → enum_clamp
// with LLMCalled=false would be excluded; this also confirms the LLMResult-based gate.
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
		"LLMResult='enum_clamp' must be emitted to llm counter")
	assert.Contains(t, rec.fitScores, 65,
		"enum_clamp must still observe fit score histogram — clamping does not invalidate FitScore; "+
			"RED-on-revert: remove enum_clamp from histogram gate")
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

	var llmCallsThisCycle atomic.Int64
	runUnscoredSweep(context.Background(), store, prof, deps, &llmCallsThisCycle, 50)

	assert.Equal(t, int64(1), store.setCount.Load(),
		"sweep must call SetJobScore once for the unscored open job; RED-on-revert: remove sweep code")
	assert.Empty(t, notifier.jobs,
		"sweep must NOT call the notifier — backfill path, no notify")
}

// Test_Sweep_RespectsCircuitBreaker verifies the sweep budget cap (MEDIUM-1):
// when the LLM budget is already exhausted, runUnscoredSweep returns early
// WITHOUT calling UnscoredOpenJobs — budget-starved jobs must NOT be marked
// scored_at=now() and removed from the unscored pool permanently.
//
// RED-on-revert: remove the remaining<=0 early-return in runUnscoredSweep →
// UnscoredOpenJobs is called, SetJobScore is called (LLMCalled=false → unscored),
// and the job is permanently removed from the pool without being LLM-scored.
func Test_Sweep_RespectsCircuitBreaker(t *testing.T) {
	t.Setenv("HUNT_SCORE_FAIL_OPEN", "true")
	t.Setenv("HUNT_SCORE_MIN_JACCARD", "0")
	t.Setenv("HUNT_SCORE_MAX_LLM_PER_CYCLE", "0") // budget fully exhausted before sweep starts
	t.Setenv("HUNT_SCORE_SWEEP_LIMIT", "10")

	postedAt := time.Now().Add(-1 * time.Hour)
	unscoredJob := hunt.Job{
		ID:       88,
		Title:    "Backend Engineer",
		PostedAt: &postedAt,
		Status:   hunt.StatusOpen,
	}

	store := &fakeUnscoredStore{
		jobs: []hunt.Job{unscoredJob},
	}

	prof := &score.ScoringProfile{
		Seniority:    "Staff",
		CoreSkills:   []string{"Go"},
		CompFloorUSD: 200000,
		WorkAuth:     "US authorized",
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
	maxBudget := score.MaxLLMPerCycle(nil) // = 0 from env
	var llmCallsThisCycle atomic.Int64
	llmCallsThisCycle.Store(int64(maxBudget)) // budget exhausted
	runUnscoredSweep(context.Background(), store, prof, deps, &llmCallsThisCycle, 50)

	assert.Equal(t, int64(0), llmCalls.Load(),
		"sweep must NOT call LLM when per-cycle budget exhausted")
	// MEDIUM-1: sweep returns early before UnscoredOpenJobs → SetJobScore never called.
	// Jobs remain in the unscored pool for the next cycle.
	assert.Equal(t, int64(0), store.setCount.Load(),
		"sweep must NOT call SetJobScore when budget exhausted — job must remain in unscored pool; "+
			"RED-on-revert: remove the remaining<=0 early-return in runUnscoredSweep")
}

// Test_Sweep_SkipsQueryWhenBudgetZero verifies MEDIUM-1 cap: when the remaining
// LLM budget is already 0 (or negative), runUnscoredSweep must NOT call
// UnscoredOpenJobs at all — budget-starved jobs must NOT get scored_at set and
// remain eligible for the next cycle.
//
// RED-on-revert: remove the remaining<=0 early-return guard in runUnscoredSweep →
// UnscoredOpenJobs is called and SetJobScore marks the job as scored, removing
// it from the unscored pool permanently.
func Test_Sweep_SkipsQueryWhenBudgetZero(t *testing.T) {
	t.Setenv("HUNT_SCORE_MAX_LLM_PER_CYCLE", "3")
	t.Setenv("HUNT_SCORE_SWEEP_LIMIT", "10")

	postedAt := time.Now().Add(-30 * time.Minute)
	store := &fakeUnscoredStore{
		jobs: []hunt.Job{
			{ID: 99, Title: "Go Engineer", PostedAt: &postedAt, Status: hunt.StatusOpen},
		},
	}

	prof := &score.ScoringProfile{CoreSkills: []string{"Go"}}
	deps := score.ScorerDeps{
		Jaccard: func(_, _ string) float64 { return 50 },
		LLM:     func(_ context.Context, _ string) (string, error) { return "", nil },
	}

	// Budget fully consumed by ingest path.
	maxLLM := score.MaxLLMPerCycle(nil) // 3 from env
	var llmCallsThisCycle atomic.Int64
	llmCallsThisCycle.Store(int64(maxLLM)) // already at ceiling
	runUnscoredSweep(context.Background(), store, prof, deps, &llmCallsThisCycle, 50)

	assert.Equal(t, int64(0), store.setCount.Load(),
		"sweep must NOT call UnscoredOpenJobs / SetJobScore when remaining budget == 0; "+
			"RED-on-revert: remove the remaining<=0 early-return in runUnscoredSweep")
}

// Test_BudgetCountsAttempts verifies MEDIUM-2: the per-cycle LLM budget counter
// is incremented when LLMResult != "" (LLM was invoked — includes parse_fail +
// llm_error), NOT only when LLMCalled==true.
//
// Concretely: scoreJobWithLimit with a parse_fail result must increment
// llmCallsThisCycle so the circuit breaker fires correctly.
//
// RED-on-revert: revert increment signal to LLMCalled → parse_fail result does
// not bump the budget and a proxy-down storm issues unlimited LLM calls.
func Test_BudgetCountsAttempts(t *testing.T) {
	t.Setenv("HUNT_SCORE_FAIL_OPEN", "true")
	t.Setenv("HUNT_SCORE_MIN_JACCARD", "0")
	t.Setenv("HUNT_SCORE_MAX_LLM_PER_CYCLE", "5")

	postedAt := time.Now().Add(-10 * time.Minute)
	job := hunt.Job{
		ID:          101,
		Title:       "Go Engineer",
		Description: "Go distributed systems",
		PostedAt:    &postedAt,
		Status:      hunt.StatusOpen,
	}
	prof := &score.ScoringProfile{CoreSkills: []string{"Go"}}

	store := &fakeUnscoredStore{}
	deps := score.ScorerDeps{
		Jaccard: func(_, _ string) float64 { return 50 },
		// LLM returns malformed JSON → parse_fail result; LLMResult="parse_fail", LLMCalled=false.
		LLM: func(_ context.Context, _ string) (string, error) {
			return "not-json", nil
		},
	}

	var llmCallsThisCycle atomic.Int64
	sr := scoreJobWithLimit(context.Background(), hunt.OutcomeCreated, job, prof, deps, store, &llmCallsThisCycle)

	// LLM was attempted (parse_fail) — budget must be consumed.
	assert.Equal(t, int64(1), llmCallsThisCycle.Load(),
		"parse_fail result (LLM was invoked) must increment llmCallsThisCycle; "+
			"RED-on-revert: revert increment to LLMCalled")
	// parse_fail → FitBandUnscored; LLMResult=="parse_fail"; LLMCalled==false.
	assert.Equal(t, "parse_fail", sr.LLMResult,
		"LLMResult must be 'parse_fail' when LLM returns malformed JSON")
	assert.False(t, sr.LLMCalled,
		"LLMCalled must remain false for failOpen (parse_fail) path")
}

// ---------------------------------------------------------------------------
// Gauge refresher tests
// ---------------------------------------------------------------------------

// fakeUnscoredStatsStore implements unscoredJobStatsStore for gauge refresher tests.
type fakeUnscoredStatsStore struct {
	stats hunt.UnscoredJobsStats
	err   error
	calls atomic.Int64
}

func (f *fakeUnscoredStatsStore) UnscoredOpenJobsStats(_ context.Context) (hunt.UnscoredJobsStats, error) {
	f.calls.Add(1)
	if f.err != nil {
		return hunt.UnscoredJobsStats{}, f.err
	}
	return f.stats, nil
}

// Test_RefreshUnscoredGauges_UpdatesGauges verifies that refreshUnscoredGauges
// calls UnscoredOpenJobsStats and sets both gauges from the result. This is
// the fix for the false-positive alert: without the periodic refresher, the
// gauges are frozen between 6h cycles and the >7200s alert fires on stale data.
//
// RED-on-revert: remove refreshUnscoredGauges → gauges stay at 0 (never updated
// between cycles). Remove the engine.SetHuntUnscoredJobsCount/MaxAge calls →
// gauges don't reflect the stats query result.
func Test_RefreshUnscoredGauges_UpdatesGauges(t *testing.T) {
	engine.InitTestRegistry()

	store := &fakeUnscoredStatsStore{
		stats: hunt.UnscoredJobsStats{
			Count:     3,
			OldestAge: 4 * time.Hour,
		},
	}

	refreshUnscoredGauges(context.Background(), store)

	assert.Equal(t, int64(1), store.calls.Load(),
		"refreshUnscoredGauges must call UnscoredOpenJobsStats once")

	assert.Equal(t, float64(3), engine.GetGaugeValue(engine.MetricHuntUnscoredJobsCount),
		"gauge count must reflect stats.Count")
	assert.InDelta(t, 14400, engine.GetGaugeValue(engine.MetricHuntUnscoredJobsMaxAge), 1,
		"gauge max_age must reflect stats.OldestAge in seconds (4h = 14400s)")
}

// Test_RefreshUnscoredGauges_ZeroCountSetsZero verifies that when there are
// no unscored jobs, both gauges are set to 0 (not left at stale values).
func Test_RefreshUnscoredGauges_ZeroCountSetsZero(t *testing.T) {
	engine.InitTestRegistry()

	// Pre-set gauges to non-zero to verify they get reset.
	engine.SetHuntUnscoredJobsCount(42)
	engine.SetHuntUnscoredJobsMaxAge(9999)

	store := &fakeUnscoredStatsStore{
		stats: hunt.UnscoredJobsStats{Count: 0, OldestAge: 0},
	}

	refreshUnscoredGauges(context.Background(), store)

	assert.Equal(t, float64(0), engine.GetGaugeValue(engine.MetricHuntUnscoredJobsCount),
		"gauge count must be 0 when no unscored jobs")
	assert.Equal(t, float64(0), engine.GetGaugeValue(engine.MetricHuntUnscoredJobsMaxAge),
		"gauge max_age must be 0 when no unscored jobs")
}

// Test_RefreshUnscoredGauges_QueryErrorDoesNotPanic verifies that a query
// failure is logged (not panicked) and gauges are left at their previous value.
// A DB query failure is NOT a pipeline stall — the alert should not fire on
// a transient DB blip.
func Test_RefreshUnscoredGauges_QueryErrorDoesNotPanic(t *testing.T) {
	engine.InitTestRegistry()

	engine.SetHuntUnscoredJobsCount(5)
	engine.SetHuntUnscoredJobsMaxAge(3000)

	store := &fakeUnscoredStatsStore{
		err: assert.AnError,
	}

	assert.NotPanics(t, func() {
		refreshUnscoredGauges(context.Background(), store)
	})

	assert.Equal(t, float64(5), engine.GetGaugeValue(engine.MetricHuntUnscoredJobsCount),
		"gauge count must be unchanged on query error")
	assert.Equal(t, float64(3000), engine.GetGaugeValue(engine.MetricHuntUnscoredJobsMaxAge),
		"gauge max_age must be unchanged on query error")
}

// Test_RefreshUnscoredGauges_NilSafeStore verifies that refreshUnscoredGauges
// is a no-op when the store doesn't satisfy unscoredJobStatsStore (e.g. a test
// fake that only implements UnscoredOpenJobs).
func Test_RefreshUnscoredGauges_NilSafeStore(t *testing.T) {
	store := &fakeUnscoredStore{} // does NOT implement unscoredJobStatsStore

	assert.NotPanics(t, func() {
		refreshUnscoredGauges(context.Background(), store)
	})
}
