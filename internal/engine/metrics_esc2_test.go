package engine

// metrics_esc2_test.go: ESC-2 observability tests for the three new gauges:
//
//   - gojob_hunt_unscored_jobs_count
//   - gojob_hunt_unscored_jobs_max_age_seconds
//   - gojob_hunt_scoring_degraded
//
// RED-on-revert:
//   - Remove any of the 3 metric consts → const/pre-touch tests fail.
//   - Remove pre-touch lines in FormatMetrics → FormatMetrics test fails.
//   - Remove SetHuntScoringDegraded → gauge setter test fails.

import (
	"strings"
	"testing"

	kitmetrics "github.com/anatolykoptev/go-kit/metrics"
)

// TestFormatMetrics_PretouchesNewGauges verifies the three ESC-2 gauges appear
// in the FormatMetrics flat-text output at 0 before any sweep/cycle runs.
func TestFormatMetrics_PretouchesNewGauges(t *testing.T) {
	orig := reg
	t.Cleanup(func() { reg = orig })
	reg = kitmetrics.NewRegistry()

	out := FormatMetrics()
	for _, name := range []string{
		MetricHuntUnscoredJobsCount,
		MetricHuntUnscoredJobsMaxAge,
		MetricHuntScoringDegraded,
	} {
		line := name + " 0"
		if !strings.Contains(out, line) {
			t.Errorf("FormatMetrics output must contain %q (pre-touched at 0); got:\n%s", line, out)
		}
	}
}

// TestSetHuntScoringDegraded_SetsGauge verifies the gauge is set to 1 for
// degraded=true and 0 for degraded=false.
func TestSetHuntScoringDegraded_SetsGauge(t *testing.T) {
	orig := reg
	t.Cleanup(func() { reg = orig })
	reg = kitmetrics.NewRegistry()

	SetHuntScoringDegraded(true)
	snap := reg.GaugeSnapshot()
	if v := snap[MetricHuntScoringDegraded]; v != 1 {
		t.Errorf("%s = %v, want 1 (degraded)", MetricHuntScoringDegraded, v)
	}

	SetHuntScoringDegraded(false)
	snap = reg.GaugeSnapshot()
	if v := snap[MetricHuntScoringDegraded]; v != 0 {
		t.Errorf("%s = %v, want 0 (healthy)", MetricHuntScoringDegraded, v)
	}
}

// TestSetHuntScoringDegraded_NilSafe verifies the setter does not panic when
// reg is nil (engine.Init not called).
func TestSetHuntScoringDegraded_NilSafe(t *testing.T) {
	orig := reg
	t.Cleanup(func() { reg = orig })
	reg = nil

	SetHuntScoringDegraded(true)
	SetHuntScoringDegraded(false)
}

// TestSetHuntUnscoredJobsCount_SetsGauge verifies the count gauge setter.
func TestSetHuntUnscoredJobsCount_SetsGauge(t *testing.T) {
	orig := reg
	t.Cleanup(func() { reg = orig })
	reg = kitmetrics.NewRegistry()

	SetHuntUnscoredJobsCount(42)
	snap := reg.GaugeSnapshot()
	if v := snap[MetricHuntUnscoredJobsCount]; v != 42 {
		t.Errorf("%s = %v, want 42", MetricHuntUnscoredJobsCount, v)
	}
}

// TestSetHuntUnscoredJobsMaxAge_SetsGauge verifies the max-age gauge setter.
func TestSetHuntUnscoredJobsMaxAge_SetsGauge(t *testing.T) {
	orig := reg
	t.Cleanup(func() { reg = orig })
	reg = kitmetrics.NewRegistry()

	SetHuntUnscoredJobsMaxAge(3600.5)
	snap := reg.GaugeSnapshot()
	if v := snap[MetricHuntUnscoredJobsMaxAge]; v != 3600.5 {
		t.Errorf("%s = %v, want 3600.5", MetricHuntUnscoredJobsMaxAge, v)
	}
}
