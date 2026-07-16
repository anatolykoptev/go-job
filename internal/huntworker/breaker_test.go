package huntworker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anatolykoptev/go-kit/breaker"
	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestWorkerWithBreaker builds a Worker whose LLM path is routed through the
// real cross-cycle breaker (newLLMBreaker) wrapping a fake llmFn. This exercises
// the REAL shipped wrapping path (breaker.Execute over w.llmFn) — the only thing
// faked is the underlying LLM transport, exactly as production fakes nothing.
func newTestWorkerWithBreaker(t *testing.T, llmFn func(ctx context.Context, prompt string) (string, error)) *Worker {
	t.Helper()
	engine.InitTestRegistry()
	w := &Worker{
		llmFn:      llmFn,
		llmBreaker: newLLMBreaker(),
	}
	w.scorerDeps.LLM = func(ctx context.Context, prompt string) (string, error) {
		return breaker.Execute(w.llmBreaker, func() (string, error) {
			return w.llmFn(ctx, prompt)
		})
	}
	return w
}

// waitForGauge polls the scoring_degraded gauge until it reaches want or the
// timeout elapses. The breaker's OnTrip/OnRecover hooks fire in a goroutine
// (breaker.go: `go b.opts.OnTrip(...)`), so the gauge update is asynchronously
// observed — a direct read would race. Returns the final value for assertion.
func waitForGauge(t *testing.T, name string, want float64, timeout time.Duration) float64 {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if engine.GetGaugeValue(name) == want {
			return want
		}
		time.Sleep(2 * time.Millisecond)
	}
	return engine.GetGaugeValue(name)
}

// TestCrossCycleBreaker_TripsAfter3Errors verifies that 3 consecutive LLM
// errors trip the cross-cycle breaker: OnTrip fires and the scoring_degraded
// gauge is set to 1.
//
// This exercises the REAL breaker wrapping path (breaker.Execute over w.llmFn)
// with FailThreshold=3 from newLLMBreaker.
func TestCrossCycleBreaker_TripsAfter3Errors(t *testing.T) {
	var calls atomic.Int32
	w := newTestWorkerWithBreaker(t, func(_ context.Context, _ string) (string, error) {
		calls.Add(1)
		return "", errors.New("llm unavailable")
	})

	for i := 0; i < 3; i++ {
		_, err := w.scorerDeps.LLM(context.Background(), "score this job")
		require.Error(t, err, "call %d must return the underlying error", i+1)
	}

	got := waitForGauge(t, engine.MetricHuntScoringDegraded, 1, 2*time.Second)
	assert.Equal(t, float64(1), got,
		"scoring_degraded gauge must be 1 after 3 consecutive LLM errors trip the breaker (OnTrip hook)")
	assert.Equal(t, int32(3), calls.Load(),
		"the underlying LLM must have been called exactly 3 times (once per error before trip)")
}

// TestCrossCycleBreaker_ResetsOnSuccess verifies that 2 errors followed by a
// success do NOT trip the breaker (consecutiveFails resets to 0 on success).
// OnTrip is never called and the gauge stays 0.
func TestCrossCycleBreaker_ResetsOnSuccess(t *testing.T) {
	var calls atomic.Int32
	w := newTestWorkerWithBreaker(t, func(_ context.Context, _ string) (string, error) {
		n := calls.Add(1)
		if n <= 2 {
			return "", errors.New("transient")
		}
		return `{"fit_score":80}`, nil
	})

	// Two errors (below FailThreshold=3).
	_, err := w.scorerDeps.LLM(context.Background(), "p")
	require.Error(t, err)
	_, err = w.scorerDeps.LLM(context.Background(), "p")
	require.Error(t, err)

	// Success resets consecutiveFails to 0.
	out, err := w.scorerDeps.LLM(context.Background(), "p")
	require.NoError(t, err)
	assert.Equal(t, `{"fit_score":80}`, out)

	// Give the (non-firing) hook goroutine a moment; gauge must stay 0.
	time.Sleep(50 * time.Millisecond)
	got := engine.GetGaugeValue(engine.MetricHuntScoringDegraded)
	assert.Equal(t, float64(0), got,
		"scoring_degraded gauge must stay 0 — breaker does NOT trip when a success resets the failure counter before reaching FailThreshold")
	assert.Equal(t, int32(3), calls.Load(),
		"underlying LLM must have been called 3 times (2 errors + 1 success)")
}

// TestCrossCycleBreaker_OpenFastFailsLLM verifies that once the breaker is open,
// subsequent LLM calls return breaker.ErrOpen immediately WITHOUT invoking the
// underlying LLM function. This is the fast-fail guarantee that prevents a
// proxy-down storm from issuing unlimited calls.
func TestCrossCycleBreaker_OpenFastFailsLLM(t *testing.T) {
	var calls atomic.Int32
	w := newTestWorkerWithBreaker(t, func(_ context.Context, _ string) (string, error) {
		calls.Add(1)
		return "", errors.New("llm unavailable")
	})

	// Trip the breaker with 3 errors.
	for i := 0; i < 3; i++ {
		_, err := w.scorerDeps.LLM(context.Background(), "p")
		require.Error(t, err)
	}
	// Wait for the trip to propagate (OnTrip fires in a goroutine).
	require.Equal(t, float64(1), waitForGauge(t, engine.MetricHuntScoringDegraded, 1, 2*time.Second),
		"breaker must be tripped before fast-fail assertions")
	callsAtTrip := calls.Load()

	// Subsequent calls must fast-fail with breaker.ErrOpen and NOT call the LLM.
	for i := 0; i < 5; i++ {
		_, err := w.scorerDeps.LLM(context.Background(), "p")
		assert.ErrorIs(t, err, breaker.ErrOpen,
			"call %d after trip must fast-fail with breaker.ErrOpen", i+1)
	}

	assert.Equal(t, callsAtTrip, calls.Load(),
		"underlying LLM must NOT be called while breaker is open (fast-fail without reaching the wrapped target)")
}

// TestCrossCycleBreaker_OnTripSetsGauge verifies the OnTrip hook sets
// scoring_degraded to 1 and, after a forced half-open + success, the OnRecover
// hook sets it back to 0.
func TestCrossCycleBreaker_OnTripSetsGauge(t *testing.T) {
	w := newTestWorkerWithBreaker(t, func(_ context.Context, _ string) (string, error) {
		return "", errors.New("llm unavailable")
	})

	// Trip the breaker.
	for i := 0; i < 3; i++ {
		_, _ = w.scorerDeps.LLM(context.Background(), "p")
	}
	got := waitForGauge(t, engine.MetricHuntScoringDegraded, 1, 2*time.Second)
	assert.Equal(t, float64(1), got,
		"OnTrip hook must set scoring_degraded gauge to 1 when the breaker trips")

	// Force half-open (operator-style reset) then succeed to trigger OnRecover.
	w.llmBreaker.ForceHalfOpen()
	w.llmFn = func(_ context.Context, _ string) (string, error) {
		return `{"fit_score":90}`, nil
	}
	_, err := w.scorerDeps.LLM(context.Background(), "probe")
	require.NoError(t, err, "half-open probe call must succeed")

	got = waitForGauge(t, engine.MetricHuntScoringDegraded, 0, 2*time.Second)
	assert.Equal(t, float64(0), got,
		"OnRecover hook must set scoring_degraded gauge back to 0 when the breaker recovers")
}
