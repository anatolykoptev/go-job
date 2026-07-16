package engine

import (
	"testing"

	kitmetrics "github.com/anatolykoptev/go-kit/metrics"
)

func TestSetOpportunityIngestMemory(t *testing.T) {
	orig := reg
	t.Cleanup(func() { reg = orig })
	reg = kitmetrics.NewRegistry()

	const want = uint64(123456789)
	SetOpportunityIngestMemory(want)
	if got := reg.GaugeSnapshot()[MetricOpportunityIngestMemory]; got != float64(want) {
		t.Fatalf("%s = %v, want %d", MetricOpportunityIngestMemory, got, want)
	}
}

func TestSetOpportunityIngestMemoryNilSafe(t *testing.T) {
	orig := reg
	t.Cleanup(func() { reg = orig })
	reg = nil
	SetOpportunityIngestMemory(1)
}
