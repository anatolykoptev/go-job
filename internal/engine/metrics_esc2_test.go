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
	"regexp"
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
	t.Cleanup(func() { reg = orig; scoringDegradedState.Store(false) })
	reg = kitmetrics.NewRegistry()
	scoringDegradedState.Store(false)

	SetHuntScoringDegraded(true, "llm_error")
	snap := reg.GaugeSnapshot()
	if v := snap[MetricHuntScoringDegraded]; v != 1 {
		t.Errorf("%s = %v, want 1 (degraded)", MetricHuntScoringDegraded, v)
	}

	SetHuntScoringDegraded(false, "cycle_reset")
	snap = reg.GaugeSnapshot()
	if v := snap[MetricHuntScoringDegraded]; v != 0 {
		t.Errorf("%s = %v, want 0 (healthy)", MetricHuntScoringDegraded, v)
	}
}

// TestSetHuntScoringDegraded_NilSafe verifies the setter does not panic when
// reg is nil (engine.Init not called).
func TestSetHuntScoringDegraded_NilSafe(t *testing.T) {
	orig := reg
	t.Cleanup(func() { reg = orig; scoringDegradedState.Store(false) })
	reg = nil
	scoringDegradedState.Store(false)

	SetHuntScoringDegraded(true, "llm_error")
	SetHuntScoringDegraded(false, "cycle_reset")
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

// TestFormatMetrics_ScoringDegradedMetricContract verifies the exposed metric
// text for gojob_hunt_scoring_degraded and gojob_hunt_scoring_degraded_total
// matches an anchored regex — NOT a strings.Contains substring check. A
// substring assertion that passes on malformed output is a shipped-blocker
// class in this fleet.
//
// The gauge must appear as a bare "hunt_scoring_degraded 0" line (no labels).
// The reason counter must appear as "hunt_scoring_degraded_total{reason=...} 0"
// for each bounded reason.
func TestFormatMetrics_ScoringDegradedMetricContract(t *testing.T) {
	orig := reg
	t.Cleanup(func() { reg = orig; scoringDegradedState.Store(false) })
	reg = kitmetrics.NewRegistry()
	scoringDegradedState.Store(false)

	out := FormatMetrics()

	// Gauge: anchored match — bare metric name + value, no labels.
	// FormatMetrics uses internal names (no gojob_ prefix); the prefix is
	// added by the Prometheus bridge, not the flat-text endpoint.
	gaugeRe := regexp.MustCompile(`(?m)^hunt_scoring_degraded 0$`)
	if !gaugeRe.MatchString(out) {
		t.Errorf("gauge contract: must match %q; got:\n%s", gaugeRe.String(), out)
	}

	// Reason counter: anchored match for each bounded reason.
	for _, reason := range []string{"breaker_open", "llm_error", "parse_fail"} {
		re := regexp.MustCompile(`(?m)^hunt_scoring_degraded_total\{reason=` + reason + `\} 0$`)
		if !re.MatchString(out) {
			t.Errorf("reason counter contract: must match %q; got:\n%s", re.String(), out)
		}
	}

	// skipped_budget must appear in the LLM result counter pre-touch.
	skipRe := regexp.MustCompile(`(?m)^hunt_score_llm_total\{result=skipped_budget\} 0$`)
	if !skipRe.MatchString(out) {
		t.Errorf("skipped_budget contract: must match %q; got:\n%s", skipRe.String(), out)
	}
}
