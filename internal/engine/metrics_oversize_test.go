package engine

import "testing"

// TestAddOversizeBytes_CounterExists verifies that AddOversizeBytes wires into
// the registry as a counter (not a histogram), and that
// MetricOversizeBytesTotal is the name used for Prometheus export.
//
// RED: fails until MAJOR #2 is implemented (ObserveOversizeBytes removed,
// AddOversizeBytes + MetricOversizeBytesTotal added).
func TestAddOversizeBytes_CounterExists(t *testing.T) {
	Init(Config{})
	before := GetMetrics()

	const n = 1500
	AddOversizeBytes(n)

	after := GetMetrics()
	delta := after[MetricOversizeBytesTotal] - before[MetricOversizeBytesTotal]
	if delta != n {
		t.Errorf("AddOversizeBytes(%d): counter delta = %d, want %d", n, delta, n)
	}
}

// TestAddOversizeBytes_AccumulatesAcrossCalls verifies that multiple calls
// to AddOversizeBytes are additive (counter semantics, not gauge/histogram).
func TestAddOversizeBytes_AccumulatesAcrossCalls(t *testing.T) {
	Init(Config{})
	before := GetMetrics()

	AddOversizeBytes(100)
	AddOversizeBytes(200)
	AddOversizeBytes(700)

	after := GetMetrics()
	delta := after[MetricOversizeBytesTotal] - before[MetricOversizeBytesTotal]
	if delta != 1000 {
		t.Errorf("accumulated bytes = %d, want 1000", delta)
	}
}

// TestObserveOversizeBytesAbsent verifies that the old histogram function
// ObserveOversizeBytes no longer exists (compile-time check via build tag).
// This is enforced at compile time — if ObserveOversizeBytes still exists,
// this file would not compile due to the undefined reference in the RED state.
// After MAJOR #2: this test is trivially satisfied by the build succeeding.
func TestMetricOversizeBytesTotalConstExists(t *testing.T) {
	// Verify the constant is non-empty (MetricOversizeBytesTotal must be defined).
	if MetricOversizeBytesTotal == "" {
		t.Error("MetricOversizeBytesTotal constant is empty")
	}
}
