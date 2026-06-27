package engine

import (
	"testing"

	kitmetrics "github.com/anatolykoptev/go-kit/metrics"
)

// TestMetricVacancyIngest_ConstDefined verifies the metric constant name.
func TestMetricVacancyIngest_ConstDefined(t *testing.T) {
	if MetricVacancyIngest != "vacancy_ingest_total" {
		t.Errorf("MetricVacancyIngest = %q, want %q", MetricVacancyIngest, "vacancy_ingest_total")
	}
	if MetricHuntPersistEnabled != "hunt_persist_enabled" {
		t.Errorf("MetricHuntPersistEnabled = %q, want %q", MetricHuntPersistEnabled, "hunt_persist_enabled")
	}
}

// TestIncrVacancyIngest_BoundedLabel asserts that valid result values land
// under their labelled counter and unknown values are silently dropped.
func TestIncrVacancyIngest_BoundedLabel(t *testing.T) {
	orig := reg
	t.Cleanup(func() { reg = orig })
	reg = kitmetrics.NewRegistry()

	for _, r := range []string{"ok", "weak", "skipped_store"} {
		IncrVacancyIngest(r)
	}
	IncrVacancyIngest("garbage") // must be dropped — cardinality guard

	snap := reg.Snapshot()
	for _, r := range []string{"ok", "weak", "skipped_store"} {
		key := MetricVacancyIngest + "{result=" + r + "}"
		if snap[key] != 1 {
			t.Errorf("%s = %d, want 1", key, snap[key])
		}
	}
	if snap[MetricVacancyIngest+"{result=garbage}"] != 0 {
		t.Errorf("unknown result must be dropped (cardinality guard)")
	}
}

// TestSetHuntPersistEnabled_GaugeReflectsValue asserts that the gauge is set
// to 1 for enabled and 0 for disabled. Tests the startup-observability
// invariant: a fresh run with no DB must show hunt_persist_enabled=0.
func TestSetHuntPersistEnabled_GaugeReflectsValue(t *testing.T) {
	orig := reg
	t.Cleanup(func() { reg = orig })
	reg = kitmetrics.NewRegistry()

	// Initially the gauge is not set — default float64 zero.
	SetHuntPersistEnabled(false)
	snap := reg.GaugeSnapshot()
	if v := snap[MetricHuntPersistEnabled]; v != 0 {
		t.Errorf("disabled gauge = %v, want 0", v)
	}

	SetHuntPersistEnabled(true)
	snap = reg.GaugeSnapshot()
	if v := snap[MetricHuntPersistEnabled]; v != 1 {
		t.Errorf("enabled gauge = %v, want 1", v)
	}

	// Toggle back to disabled.
	SetHuntPersistEnabled(false)
	snap = reg.GaugeSnapshot()
	if v := snap[MetricHuntPersistEnabled]; v != 0 {
		t.Errorf("re-disabled gauge = %v, want 0", v)
	}
}
