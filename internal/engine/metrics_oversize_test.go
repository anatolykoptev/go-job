package engine

import (
	"strings"
	"testing"

	kitmetrics "github.com/anatolykoptev/go-kit/metrics"
)

// TestObserveOversizeBytes_HistogramAppears verifies that after Observe the
// in-memory (non-prom) registry captures the value in a Reservoir.
//
// Uses kitmetrics.NewRegistry (no prom bridge) to avoid DefaultRegisterer
// collisions across parallel test runs and to exercise the reservoir path.
func TestObserveOversizeBytes_HistogramAppears(t *testing.T) {
	testReg := kitmetrics.NewRegistry()
	testReg.Observe(MetricOversizeBytes, 50000)

	snap := testReg.HistogramSnapshot()
	h, ok := snap[MetricOversizeBytes]
	if !ok {
		t.Fatalf("histogram %q not found in snapshot after Observe", MetricOversizeBytes)
	}
	if h.Count != 1 {
		t.Errorf("histogram count = %d, want 1", h.Count)
	}
	if h.Max != 50000 {
		t.Errorf("histogram max = %v, want 50000", h.Max)
	}
}

// TestMetricOversizeBytes_ConstDefined verifies the constant is non-empty.
func TestMetricOversizeBytes_ConstDefined(t *testing.T) {
	if MetricOversizeBytes == "" {
		t.Error("MetricOversizeBytes constant is empty")
	}
}

// TestOversizeBytesBuckets_StrictlyAscending verifies OversizeBytesBuckets
// is valid (strictly ascending, all finite) — mirrors the validation in
// go-kit RegisterHistogram to give a local failure rather than a panic.
func TestOversizeBytesBuckets_StrictlyAscending(t *testing.T) {
	if len(OversizeBytesBuckets) == 0 {
		t.Fatal("OversizeBytesBuckets is empty")
	}
	for i := 1; i < len(OversizeBytesBuckets); i++ {
		if OversizeBytesBuckets[i] <= OversizeBytesBuckets[i-1] {
			t.Errorf("bucket[%d]=%v not greater than bucket[%d]=%v",
				i, OversizeBytesBuckets[i], i-1, OversizeBytesBuckets[i-1])
		}
	}
}

// TestOversizeBytesBuckets_BoundaryValues verifies the log-scale bucket
// semantics: 50000 bytes must fall in the le=65536 bucket but not le=16384.
//
// This tests the boundary table directly (not a prom Gather) to avoid
// DefaultRegisterer collisions across parallel runs when no
// NewPrometheusRegistryOn API is available.
func TestOversizeBytesBuckets_BoundaryValues(t *testing.T) {
	const payload = 50000.0

	var inLE16384, inLE65536 bool
	for _, b := range OversizeBytesBuckets {
		if b == 16384 {
			inLE16384 = payload <= b
		}
		if b == 65536 {
			inLE65536 = payload <= b
		}
	}
	if inLE16384 {
		t.Errorf("payload %.0f should NOT fall in le=16384 bucket, but does", payload)
	}
	if !inLE65536 {
		t.Errorf("payload %.0f should fall in le=65536 bucket, but does not", payload)
	}
}

// TestObserveOversizeBytes_WiredThroughEngineReg verifies that ObserveOversizeBytes
// does not panic when called after Init.
func TestObserveOversizeBytes_WiredThroughEngineReg(t *testing.T) {
	// Init with empty config sets up a prom-backed reg.
	Init(Config{})
	// Should not panic.
	ObserveOversizeBytes(4096)
}

// TestOversizeBytesTotalGone verifies MetricOversizeBytesTotal no longer
// appears in FormatMetrics output.
func TestOversizeBytesTotalGone(t *testing.T) {
	Init(Config{})
	output := FormatMetrics()
	if strings.Contains(output, "oversize_bytes_total") {
		t.Errorf("FormatMetrics still references oversize_bytes_total; counter must be removed")
	}
}
