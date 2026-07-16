package engine

// metrics_hunt_health_test.go: fitness-function tests for the three hunt
// health/safety metrics added during the bug-hunt remediation:
//
//  1. hunt_score_breaker_trips_total counter (IncrHuntScoreBreakerTrips)
//  2. hunt_score_persist_failures_total counter (IncrHuntScorePersistFailures)
//  3. hunt_notify_health gauge (SetHuntNotifyHealth)
//
// RED-on-revert:
//   - Remove MetricHuntScoreBreakerTrips const → const test fails.
//   - Remove IncrHuntScoreBreakerTrips → counter never lands → test fails.
//   - Remove MetricHuntScorePersistFailures const → const test fails.
//   - Remove IncrHuntScorePersistFailures → counter never lands → test fails.
//   - Remove MetricHuntNotifyHealth const → const test fails.
//   - Remove SetHuntNotifyHealth → gauge never lands → test fails.

import (
	"strings"
	"testing"

	kitmetrics "github.com/anatolykoptev/go-kit/metrics"
)

// --- Const tests ---

func TestMetricHuntScoreBreakerTrips_ConstDefined(t *testing.T) {
	if MetricHuntScoreBreakerTrips != "hunt_score_breaker_trips_total" {
		t.Errorf("MetricHuntScoreBreakerTrips = %q, want %q",
			MetricHuntScoreBreakerTrips, "hunt_score_breaker_trips_total")
	}
}

func TestMetricHuntScorePersistFailures_ConstDefined(t *testing.T) {
	if MetricHuntScorePersistFailures != "hunt_score_persist_failures_total" {
		t.Errorf("MetricHuntScorePersistFailures = %q, want %q",
			MetricHuntScorePersistFailures, "hunt_score_persist_failures_total")
	}
}

func TestMetricHuntNotifyHealth_ConstDefined(t *testing.T) {
	if MetricHuntNotifyHealth != "hunt_notify_health" {
		t.Errorf("MetricHuntNotifyHealth = %q, want %q",
			MetricHuntNotifyHealth, "hunt_notify_health")
	}
}

// --- Counter emission tests ---

func TestIncrHuntScoreBreakerTrips_Lands(t *testing.T) {
	orig := reg
	t.Cleanup(func() { reg = orig })
	reg = kitmetrics.NewRegistry()

	IncrHuntScoreBreakerTrips()
	IncrHuntScoreBreakerTrips()

	snap := reg.Snapshot()
	if snap[MetricHuntScoreBreakerTrips] != 2 {
		t.Errorf("%s = %d, want 2", MetricHuntScoreBreakerTrips, snap[MetricHuntScoreBreakerTrips])
	}
}

func TestIncrHuntScorePersistFailures_Lands(t *testing.T) {
	orig := reg
	t.Cleanup(func() { reg = orig })
	reg = kitmetrics.NewRegistry()

	IncrHuntScorePersistFailures()

	snap := reg.Snapshot()
	if snap[MetricHuntScorePersistFailures] != 1 {
		t.Errorf("%s = %d, want 1", MetricHuntScorePersistFailures, snap[MetricHuntScorePersistFailures])
	}
}

// --- Gauge emission test ---

func TestSetHuntNotifyHealth_Lands(t *testing.T) {
	orig := reg
	t.Cleanup(func() { reg = orig })
	reg = kitmetrics.NewRegistry()

	SetHuntNotifyHealth(true)
	snap := reg.GaugeSnapshot()
	if v := snap[MetricHuntNotifyHealth]; v != 1 {
		t.Errorf("%s = %v, want 1 (healthy)", MetricHuntNotifyHealth, v)
	}

	SetHuntNotifyHealth(false)
	snap = reg.GaugeSnapshot()
	if v := snap[MetricHuntNotifyHealth]; v != 0 {
		t.Errorf("%s = %v, want 0 (unhealthy)", MetricHuntNotifyHealth, v)
	}
}

// TestSetHuntNotifyHealth_NilSafe verifies the gauge setter does not panic
// when reg is nil (engine.Init not called — e.g. in tests that exercise
// main.go startup paths without a full engine).
func TestSetHuntNotifyHealth_NilSafe(t *testing.T) {
	orig := reg
	t.Cleanup(func() { reg = orig })
	reg = nil

	// Must not panic.
	SetHuntNotifyHealth(true)
	SetHuntNotifyHealth(false)
}

// --- Metric key registration test ---

// TestHuntHealthMetrics_InFormatKeys verifies the two counter metrics appear
// in the FormatMetrics output (the /metrics text endpoint). The gauge
// (hunt_notify_health) is not in FormatMetrics — it uses GaugeSnapshot, not
// the counter Snapshot that FormatMetrics iterates.
func TestHuntHealthMetrics_InFormatKeys(t *testing.T) {
	orig := reg
	t.Cleanup(func() { reg = orig })
	reg = kitmetrics.NewRegistry()

	IncrHuntScoreBreakerTrips()
	IncrHuntScorePersistFailures()

	out := FormatMetrics()
	for _, k := range []string{
		MetricHuntScoreBreakerTrips,
		MetricHuntScorePersistFailures,
	} {
		if !strings.Contains(out, k) {
			t.Errorf("metric %q not found in FormatMetrics output", k)
		}
	}
}
