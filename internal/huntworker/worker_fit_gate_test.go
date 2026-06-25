package huntworker

// worker_fit_gate_test.go: fitness-function #1 — the fit gate.
//
// Gate table (Phase 5 spec):
//   score==nil (scoring disabled)   → notify (recency-only card), outcome "sent" (existing)
//   score.FitBand=="unscored" (LLM fail) → notify (degraded card), outcome "unscored"
//   score.FitScore < MIN_FIT (real band) → skip notify, outcome "low_fit"
//   else                            → notify (full fit-card), outcome "sent"
//
// RED-on-revert:
//   Remove the fit gate in maybeNotifyJob → low_fit case calls notifier, test fails.
//   Remove the unscored outcome metric → outcome stays "sent", test fails.
//   Remove nil-score notify path → nil-score case does not call notifier, test fails.

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/stretchr/testify/assert"
)

// fakeFitNotifier records NotifyNewJob calls and the score passed to them.
// Implements hunt.Notifier so it can be wired into the worker.
type fakeFitNotifier struct {
	callCount atomic.Int32
	scores    []*hunt.ScoreResult // nil entry = nil score was passed
}

func (f *fakeFitNotifier) NotifyNewBounty(b hunt.Bounty)        {}
func (f *fakeFitNotifier) NotifyNewFreelance(fr hunt.Freelance) {}
func (f *fakeFitNotifier) NotifyNewSecurity(s hunt.Security)    {}
func (f *fakeFitNotifier) NotifyNewJob(j hunt.Job, sr *hunt.ScoreResult) {
	f.callCount.Add(1)
	f.scores = append(f.scores, sr)
}

// fakeMetricSink records IncrHuntNotify calls (outcome strings).
type fakeMetricSink struct {
	outcomes []string
}

func (f *fakeMetricSink) record(outcome string) {
	f.outcomes = append(f.outcomes, outcome)
}

// freshJob builds a Job posted 1h ago so the recency gate passes.
func freshJob() hunt.Job {
	postedAt := time.Now().Add(-1 * time.Hour)
	return hunt.Job{
		ID:      99,
		Title:   "Senior Go Engineer",
		Company: "Acme",
		URL:     "https://jobs.acme.io/1",
		Source:  "greenhouse",
		Status:  hunt.StatusOpen,
		PostedAt: &postedAt,
	}
}

// Test_Gate_DropsLowFit covers all 4 gate outcomes.
//
// Case 1: FitScore=40, MIN_FIT=60 → notifier NOT called, outcome "low_fit"
// Case 2: FitScore=80, MIN_FIT=60 → notifier called, outcome "sent"
// Case 3: FitBand="unscored", MIN_FIT=60 → notifier called (degraded card), outcome "unscored"
// Case 4: score=nil (disabled) → notifier called (recency-only card), outcome "sent"
func Test_Gate_DropsLowFit(t *testing.T) {
	t.Run("low_fit_drops_notify", func(t *testing.T) {
		t.Setenv("HUNT_NOTIFY_MIN_FIT", "60")

		metrics := &fakeMetricSink{}
		notifier := &fakeFitNotifier{}
		w := &Worker{
			notifier:     notifier,
			notifyMetric: metrics.record,
		}

		sr := &hunt.ScoreResult{FitScore: 40, FitBand: "medium"}
		w.maybeNotifyJob(freshJob(), hunt.OutcomeCreated, sr)

		assert.Zero(t, notifier.callCount.Load(), "low_fit (40 < 60): notifier must NOT be called")
		assert.Contains(t, metrics.outcomes, "low_fit", "low_fit must be recorded in metrics")
	})

	t.Run("above_threshold_sends", func(t *testing.T) {
		t.Setenv("HUNT_NOTIFY_MIN_FIT", "60")

		metrics := &fakeMetricSink{}
		notifier := &fakeFitNotifier{}
		w := &Worker{
			notifier:     notifier,
			notifyMetric: metrics.record,
		}

		sr := &hunt.ScoreResult{FitScore: 80, FitBand: "high"}
		w.maybeNotifyJob(freshJob(), hunt.OutcomeCreated, sr)

		assert.Equal(t, int32(1), notifier.callCount.Load(), "fit≥threshold (80≥60): notifier must be called once")
		assert.Len(t, notifier.scores, 1)
		assert.Equal(t, sr, notifier.scores[0], "full ScoreResult must be threaded to notifier")
	})

	t.Run("unscored_sends_degraded", func(t *testing.T) {
		t.Setenv("HUNT_NOTIFY_MIN_FIT", "60")

		metrics := &fakeMetricSink{}
		notifier := &fakeFitNotifier{}
		w := &Worker{
			notifier:     notifier,
			notifyMetric: metrics.record,
		}

		sr := &hunt.ScoreResult{FitBand: "unscored"}
		w.maybeNotifyJob(freshJob(), hunt.OutcomeCreated, sr)

		assert.Equal(t, int32(1), notifier.callCount.Load(), "unscored (LLM fail): notifier must be called (fail-open)")
		assert.Contains(t, metrics.outcomes, "unscored", "unscored outcome must be recorded in metrics")
	})

	t.Run("nil_score_sends_recency_only", func(t *testing.T) {
		t.Setenv("HUNT_NOTIFY_MIN_FIT", "60")

		metrics := &fakeMetricSink{}
		notifier := &fakeFitNotifier{}
		w := &Worker{
			notifier:     notifier,
			notifyMetric: metrics.record,
		}

		// nil score = scoring disabled
		w.maybeNotifyJob(freshJob(), hunt.OutcomeCreated, nil)

		assert.Equal(t, int32(1), notifier.callCount.Load(), "nil score: notifier must be called (recency-only card)")
		assert.Len(t, notifier.scores, 1)
		assert.Nil(t, notifier.scores[0], "nil score must be threaded as nil to notifier")
	})
}
