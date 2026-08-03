package huntworker

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/anatolykoptev/go_job/internal/hunt/score"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeHuntNotifier records NotifyNewJob calls for worker unit tests.
type fakeHuntNotifier struct {
	jobs []hunt.Job
}

func (f *fakeHuntNotifier) NotifyNewBounty(b hunt.Bounty)                {}
func (f *fakeHuntNotifier) NotifyNewJob(j hunt.Job, _ *hunt.ScoreResult) { f.jobs = append(f.jobs, j) }
func (f *fakeHuntNotifier) NotifyNewFreelance(fr hunt.Freelance)         {}
func (f *fakeHuntNotifier) NotifyNewSecurity(s hunt.Security)            {}

// TestWorker_SetNotifier_Wires verifies SetNotifier assigns the notifier field.
func TestWorker_SetNotifier_Wires(t *testing.T) {
	w := &Worker{}
	n := &fakeHuntNotifier{}
	w.SetNotifier(n)
	assert.Equal(t, n, w.notifier, "SetNotifier must wire the notifier field")
}

// TestWorker_MaybeNotifyJob_Created_Open: OutcomeCreated + StatusOpen → notify fires.
func TestWorker_MaybeNotifyJob_Created_Open(t *testing.T) {
	f := &fakeHuntNotifier{}
	w := &Worker{notifier: f}
	j := hunt.Job{URL: "https://x.com/j", Status: hunt.StatusOpen}
	w.maybeNotifyJob(j, hunt.OutcomeCreated, nil)
	assert.Len(t, f.jobs, 1, "OutcomeCreated + open status must notify")
}

// TestWorker_MaybeNotifyJob_Created_EmptyStatus: empty Status treated as open (SearxngResultToHuntJob leaves Status="").
func TestWorker_MaybeNotifyJob_Created_EmptyStatus(t *testing.T) {
	f := &fakeHuntNotifier{}
	w := &Worker{notifier: f}
	j := hunt.Job{URL: "https://x.com/j", Status: ""}
	w.maybeNotifyJob(j, hunt.OutcomeCreated, nil)
	assert.Len(t, f.jobs, 1, "empty Status must be treated as open — SearxngResultToHuntJob leaves Status empty")
}

// TestWorker_MaybeNotifyJob_Merged_NoNotify: OutcomeMerged must not notify.
func TestWorker_MaybeNotifyJob_Merged_NoNotify(t *testing.T) {
	f := &fakeHuntNotifier{}
	w := &Worker{notifier: f}
	j := hunt.Job{URL: "https://x.com/j", Status: hunt.StatusOpen}
	w.maybeNotifyJob(j, hunt.OutcomeMerged, nil)
	assert.Empty(t, f.jobs, "OutcomeMerged must not notify")
}

// TestWorker_MaybeNotifyJob_NilNotifier: no panic when notifier is nil, and
// the "notifier_disabled" metric is emitted so operators can alert on it.
func TestWorker_MaybeNotifyJob_NilNotifier(t *testing.T) {
	var metricOutcomes []string
	w := &Worker{
		notifier: nil,
		notifyMetric: func(outcome string) {
			metricOutcomes = append(metricOutcomes, outcome)
		},
	}
	j := hunt.Job{URL: "https://x.com/j", Status: hunt.StatusOpen}
	w.maybeNotifyJob(j, hunt.OutcomeCreated, nil) // must not panic
	assert.Contains(t, metricOutcomes, "notifier_disabled",
		"nil notifier must emit notifier_disabled metric so operators can alert")
}

// TestWorker_MaybeNotifyJob_Closed_NoNotify: closed job must not notify even on create.
func TestWorker_MaybeNotifyJob_Closed_NoNotify(t *testing.T) {
	f := &fakeHuntNotifier{}
	w := &Worker{notifier: f}
	j := hunt.Job{URL: "https://x.com/j", Status: hunt.StatusClosed}
	w.maybeNotifyJob(j, hunt.OutcomeCreated, nil)
	assert.Empty(t, f.jobs, "closed status must not notify")
}

func TestParseQueries_Basic(t *testing.T) {
	got := parseQueries("golang developer, backend engineer, ")
	assert.Equal(t, []string{"golang developer", "backend engineer"}, got)
}

func TestParseQueries_Empty_UsesDefault(t *testing.T) {
	got := parseQueries("")
	assert.NotEmpty(t, got)
	// Default must not contain any ATS slugs (fitness function: no boards.greenhouse.io/X literals).
	for _, q := range got {
		assert.NotContains(t, q, "boards.greenhouse.io/")
		assert.NotContains(t, q, "jobs.lever.co/")
		assert.NotContains(t, q, "jobs.ashbyhq.com/")
	}
}

func TestHuntIngestEnabled_DefaultFalse(t *testing.T) {
	// HUNT_INGEST_ENABLED is not set in the test environment.
	t.Setenv("HUNT_INGEST_ENABLED", "")
	s := LoadSettings(context.Background(), nil)
	assert.False(t, s.Enabled)
}

func TestHuntIngestEnabled_TrueWhenSet(t *testing.T) {
	t.Setenv("HUNT_INGEST_ENABLED", "true")
	s := LoadSettings(context.Background(), nil)
	assert.True(t, s.Enabled)
}

func TestNewWorker_NilStore_ReturnsNil(t *testing.T) {
	w := NewWorker(nil)
	assert.Nil(t, w)
}

// TestNoCompanyTargetingInDefaults is the fitness function (ADR-002 / P1 design):
// go-job is a PUBLIC repo — personal target companies must never be baked into
// the shipped default queries.  The check covers both URL-slug form AND bare
// company names, so re-introducing targeting under either form fails the test.
//
// The set below is NOT exhaustive — it is sampled representative examples of
// the class of company-specific strings that must be absent.  When adding a new
// test query, prefer generic role/skill language, not employer names.
// TestPerPlatformTimeout_ExceedsRawWebSearchServerCap pins that the ENCLOSING
// per-platform deadline (the platCtx created in runCycle) exceeds go-search's
// raw_web_search server-side ToolTimeout (90s).
//
// Why this test is needed (distinct from TestDefaultDiscoveryTimeout_ExceedsRawWebSearchServerCap
// in the discovery package, which only checks the LEAF const):
// context.WithTimeout honours the EARLIER deadline.  If platCtx has 45s and the
// leaf adds a 100s child timeout, the effective deadline is min(45s, 100s) = 45s —
// below the server cap.  go-search's 90s ToolTimeout fires AFTER go-job has already
// given up, so the Degraded=true response never arrives and the
// source="degraded-fallback" metric stays permanently near-zero in the huntworker.
//
// RED-on-revert: restoring perPlatformTimeout = 45 * time.Second makes this test
// fail immediately (45s ≤ 90s).
func TestPerPlatformTimeout_ExceedsRawWebSearchServerCap(t *testing.T) {
	// rawWebSearchServerCap is go-search's server-side ToolTimeout — the deadline
	// within which go-search must respond before it returns a 200+Degraded=true.
	// Any enclosing go-job deadline at or below this value means Degraded never
	// arrives and the degraded-fallback metric is unreachable.
	const rawWebSearchServerCap = 90 * time.Second
	if perPlatformTimeout <= rawWebSearchServerCap {
		t.Fatalf(
			"perPlatformTimeout (%v) must exceed go-search raw_web_search server ToolTimeout (%v): "+
				"context.WithTimeout honours the EARLIER deadline, so the enclosing platCtx clamps "+
				"discovery to min(perPlatformTimeout, defaultDiscoveryTimeout); "+
				"if perPlatformTimeout ≤ server cap, Degraded responses never arrive before go-job "+
				"times out and source=\"degraded-fallback\" stays permanently near-zero; "+
				"increase perPlatformTimeout above %v",
			perPlatformTimeout, rawWebSearchServerCap, rawWebSearchServerCap,
		)
	}
}

func TestNoCompanyTargetingInDefaults(t *testing.T) {
	// Representative company names / ATS slugs that must never appear in defaults.
	// These are PUBLIC well-known entities — listing them here is NOT a personal
	// target list; it is an enumeration of the class of strings to block.
	forbiddenPatterns := []string{
		// URL-slug forms (any ATS).
		"boards.greenhouse.io/",
		"jobs.lever.co/",
		"jobs.ashbyhq.com/",
		// Symbolic guard vars that might sneak in.
		"seedOrgs", "knownOrgs",
		// Bare company names (representative sample of the class).
		// If these appear in a query string it means someone added company-specific targeting.
		"stripe", "openai", "anthropic", "google", "apple", "meta",
		"netflix", "airbnb", "uber", "lyft", "coinbase",
	}

	queries := parseQueries(defaultIngestQueries)
	require.NotEmpty(t, queries, "default queries must not be empty")

	for _, q := range queries {
		lower := strings.ToLower(q)
		for _, forbidden := range forbiddenPatterns {
			assert.NotContains(t, lower, strings.ToLower(forbidden),
				"default query %q must not contain company-specific targeting %q (PUBLIC repo — ADR-002)",
				q, forbidden)
		}
	}
}

// fakeScoreSetter records SetJobScore calls for scoring tests.
type fakeScoreSetter struct {
	mu       sync.Mutex
	calls    []int64
	scoredAt []time.Time
}

func (f *fakeScoreSetter) SetJobScore(_ context.Context, id int64, sr hunt.ScoreResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, id)
	f.scoredAt = append(f.scoredAt, sr.ScoredAt)
	return nil
}

func (f *fakeScoreSetter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// TestScoreJobWithLimit_CircuitBreakerTripped_DoesNotPersistScoredAt verifies
// that when the LLM circuit breaker trips (llmCallsThisCycle >= maxLLM), the
// job is NOT persisted via SetJobScore — leaving scored_at=NULL so the sweep
// can retry it in the next cycle. Previously, the breaker-tripped path called
// SetJobScore with ScoredAt=NOW(), permanently stranding the job outside the
// unscored pool.
//
// RED-on-revert: restoring the SetJobScore call in the breaker-tripped branch
// makes this test fail (callCount > 0).
func TestScoreJobWithLimit_CircuitBreakerTripped_DoesNotPersistScoredAt(t *testing.T) {
	store := &fakeScoreSetter{}
	var llmCalls atomic.Int64
	llmCalls.Store(int64(score.MaxLLMPerCycle(nil))) // breaker tripped: at capacity

	job := hunt.Job{ID: 42}
	result := scoreJobWithLimit(context.Background(), hunt.OutcomeCreated, job, nil, score.ScorerDeps{}, store, &llmCalls)

	// The result pointer is still returned for notification/metric purposes.
	require.NotNil(t, result, "breaker-tripped result must be returned for notification")
	assert.Equal(t, hunt.FitBandUnscored, result.FitBand, "breaker-tripped result must be unscored")

	// Critical: SetJobScore must NOT be called — scored_at stays NULL so the
	// sweep can pick up the job in the next cycle.
	assert.Equal(t, 0, store.callCount(), "SetJobScore must NOT be called when circuit breaker trips — job must stay in unscored pool (scored_at IS NULL)")
}

// fakeUnscoredJobStore implements unscoredJobStore for sweep tests.
// It returns a fixed set of jobs from UnscoredOpenJobs and records SetJobScore
// calls via the embedded fakeScoreSetter.
type fakeUnscoredJobStore struct {
	fakeScoreSetter
	jobs []hunt.Job
	err  error
}

func (f *fakeUnscoredJobStore) UnscoredOpenJobs(_ context.Context, _ int, _ bool) ([]hunt.Job, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.jobs, nil
}

// TestRunUnscoredSweep_SetsGauges verifies that runUnscoredSweep sets the
// gojob_hunt_unscored_jobs_count and gojob_hunt_unscored_jobs_max_age_seconds
// gauges from the UnscoredOpenJobs result (no extra SQL query).
//
// RED-on-revert: remove the gauge-setting block in runUnscoredSweep → gauges
// stay at 0 (or missing) and this test fails.
func TestRunUnscoredSweep_SetsGauges(t *testing.T) {
	engine.InitTestRegistry()

	// Two unscored jobs with different first_seen_at timestamps.
	oldest := time.Now().Add(-2 * time.Hour)
	newer := time.Now().Add(-30 * time.Minute)
	jobs := []hunt.Job{
		{ID: 1, FirstSeenAt: oldest},
		{ID: 2, FirstSeenAt: newer},
	}

	store := &fakeUnscoredJobStore{
		jobs: jobs,
	}
	var llmCalls atomic.Int64

	runUnscoredSweep(context.Background(), store, nil, score.ScorerDeps{}, &llmCalls, 50)

	// Count gauge must reflect the number of jobs returned.
	countVal := engine.GetGaugeValue(engine.MetricHuntUnscoredJobsCount)
	assert.Equal(t, float64(2), countVal,
		"unscored jobs count gauge must be set to the number of jobs returned by UnscoredOpenJobs")

	// Max-age gauge must reflect the age of the OLDEST job (min first_seen_at).
	maxAgeVal := engine.GetGaugeValue(engine.MetricHuntUnscoredJobsMaxAge)
	expectedAge := time.Since(oldest).Seconds()
	// Allow a small tolerance for execution time between setting and checking.
	assert.InDelta(t, expectedAge, maxAgeVal, 5.0,
		"unscored jobs max-age gauge must be set to the age of the oldest unscored job (≈ %v seconds)", expectedAge)
}

// TestScoringDegraded_LLMError_SetsGauge verifies that when scoreJobIfCreated
// encounters an LLM error (LLM returns error), the fail-open path is taken and
// engine.SetHuntScoringDegraded(true) is called — the gojob_hunt_scoring_degraded
// gauge becomes 1.
//
// This exercises the REAL code path in scoreJobIfCreated → score.Score →
// runLLMStage → failOpen("llm_error"), not a hand-copy.
//
// RED-on-revert: remove the SetHuntScoringDegraded(true) call in the
// llm_error/parse_fail branch of scoreJobIfCreated → gauge stays 0, test fails.
func TestScoringDegraded_LLMError_SetsGauge(t *testing.T) {
	engine.InitTestRegistry()
	t.Setenv("HUNT_SCORE_FAIL_OPEN", "true")
	t.Setenv("HUNT_SCORE_MIN_JACCARD", "0") // pass Jaccard gate so LLM stage is reached

	store := &fakeScoreSetter{}
	postedAt := time.Now().Add(-1 * time.Hour)
	job := hunt.Job{
		ID:          77,
		Title:       "Senior Go Engineer",
		Description: "Go Rust PostgreSQL distributed systems",
		PostedAt:    &postedAt,
	}
	prof := &score.ScoringProfile{
		Seniority:  "Staff",
		CoreSkills: []string{"Go", "Rust"},
	}
	deps := score.ScorerDeps{
		Jaccard: func(kw, text string) float64 { return 50 },
		LLM: func(_ context.Context, _ string) (string, error) {
			return "", assert.AnError // LLM returns error → llm_error fail-open path
		},
	}

	result := scoreJobIfCreated(context.Background(), hunt.OutcomeCreated, job, prof, deps, store)

	assert.Equal(t, "llm_error", result.LLMResult,
		"LLM error must produce LLMResult='llm_error' (fail-open path)")
	assert.Equal(t, hunt.FitBandUnscored, result.FitBand,
		"llm_error fail-open must land as unscored")

	got := engine.GetGaugeValue(engine.MetricHuntScoringDegraded)
	assert.Equal(t, float64(1), got,
		"gojob_hunt_scoring_degraded gauge must be 1 when the llm_error fail-open path is taken in scoreJobIfCreated")
}

// TestScoringDegraded_ParseFail_SetsGauge verifies that when scoreJobIfCreated
// encounters a JSON parse failure (LLM returns non-JSON), the fail-open path is
// taken and engine.SetHuntScoringDegraded(true) is called — the
// gojob_hunt_scoring_degraded gauge becomes 1.
//
// RED-on-revert: remove the SetHuntScoringDegraded(true) call in the
// llm_error/parse_fail branch of scoreJobIfCreated → gauge stays 0, test fails.
func TestScoringDegraded_ParseFail_SetsGauge(t *testing.T) {
	engine.InitTestRegistry()
	t.Setenv("HUNT_SCORE_FAIL_OPEN", "true")
	t.Setenv("HUNT_SCORE_MIN_JACCARD", "0") // pass Jaccard gate so LLM stage is reached

	store := &fakeScoreSetter{}
	postedAt := time.Now().Add(-1 * time.Hour)
	job := hunt.Job{
		ID:          78,
		Title:       "Senior Go Engineer",
		Description: "Go Rust PostgreSQL distributed systems",
		PostedAt:    &postedAt,
	}
	prof := &score.ScoringProfile{
		Seniority:  "Staff",
		CoreSkills: []string{"Go", "Rust"},
	}
	deps := score.ScorerDeps{
		Jaccard: func(kw, text string) float64 { return 50 },
		LLM: func(_ context.Context, _ string) (string, error) {
			return "Sorry, I cannot score this job.", nil // non-JSON → parse_fail fail-open path
		},
	}

	result := scoreJobIfCreated(context.Background(), hunt.OutcomeCreated, job, prof, deps, store)

	assert.Equal(t, "parse_fail", result.LLMResult,
		"non-JSON LLM response must produce LLMResult='parse_fail' (fail-open path)")
	assert.Equal(t, hunt.FitBandUnscored, result.FitBand,
		"parse_fail fail-open must land as unscored")

	got := engine.GetGaugeValue(engine.MetricHuntScoringDegraded)
	assert.Equal(t, float64(1), got,
		"gojob_hunt_scoring_degraded gauge must be 1 when the parse_fail fail-open path is taken in scoreJobIfCreated")
}

// TestScoringDegraded_HealthyLLM_DoesNotSetGauge verifies that a successful LLM
// score does NOT set the degraded gauge (it stays 0). This is the negative
// control for the two tests above.
func TestScoringDegraded_HealthyLLM_DoesNotSetGauge(t *testing.T) {
	engine.InitTestRegistry()
	t.Setenv("HUNT_SCORE_FAIL_OPEN", "true")
	t.Setenv("HUNT_SCORE_MIN_JACCARD", "0")

	store := &fakeScoreSetter{}
	postedAt := time.Now().Add(-1 * time.Hour)
	job := hunt.Job{
		ID:          79,
		Title:       "Senior Go Engineer",
		Description: "Go Rust PostgreSQL distributed systems",
		PostedAt:    &postedAt,
	}
	prof := &score.ScoringProfile{
		Seniority:  "Staff",
		CoreSkills: []string{"Go", "Rust"},
	}
	deps := score.ScorerDeps{
		Jaccard: func(kw, text string) float64 { return 50 },
		LLM: func(_ context.Context, _ string) (string, error) {
			return `{"fit_score":80,"fit_reasons":["Go match"],"fit_gaps":[],"success_band":"MODERATE","success_reasoning":"good match","over_under":"well_matched"}`, nil
		},
	}

	result := scoreJobIfCreated(context.Background(), hunt.OutcomeCreated, job, prof, deps, store)

	assert.Equal(t, "ok", result.LLMResult, "successful LLM must produce LLMResult='ok'")

	got := engine.GetGaugeValue(engine.MetricHuntScoringDegraded)
	assert.Equal(t, float64(0), got,
		"gojob_hunt_scoring_degraded gauge must stay 0 when LLM scoring succeeds (no fail-open)")
}

// TestRunUnscoredSweep_SetsGauges_EmptyResult verifies the gauges are set to 0
// when UnscoredOpenJobs returns no jobs.
func TestRunUnscoredSweep_SetsGauges_EmptyResult(t *testing.T) {
	engine.InitTestRegistry()

	store := &fakeUnscoredJobStore{
		jobs: nil,
	}
	var llmCalls atomic.Int64

	runUnscoredSweep(context.Background(), store, nil, score.ScorerDeps{}, &llmCalls, 50)

	countVal := engine.GetGaugeValue(engine.MetricHuntUnscoredJobsCount)
	assert.Equal(t, float64(0), countVal,
		"unscored jobs count gauge must be 0 when no unscored jobs are found")

	maxAgeVal := engine.GetGaugeValue(engine.MetricHuntUnscoredJobsMaxAge)
	assert.Equal(t, float64(0), maxAgeVal,
		"unscored jobs max-age gauge must be 0 when no unscored jobs are found")
}
