package engine

import (
	"strings"
	"testing"

	kitmetrics "github.com/anatolykoptev/go-kit/metrics"
)

func TestWarmAlertBoundedMetrics_PreRegistersAccountsConstrainRefusals(t *testing.T) {
	orig := reg
	t.Cleanup(func() { reg = orig })
	reg = kitmetrics.NewRegistry()

	warmAlertBoundedMetrics()

	snap := reg.Snapshot()
	if v, ok := snap[MetricAccountsConstrainRefusals]; !ok || v != 0 {
		t.Fatalf("series %q missing/non-zero after warm-up (present=%v value=%d)",
			MetricAccountsConstrainRefusals, ok, v)
	}
}

func TestIncrAccountsConstrainRefusals_IncrementsAndFormats(t *testing.T) {
	orig := reg
	t.Cleanup(func() { reg = orig })
	reg = kitmetrics.NewRegistry()
	warmAlertBoundedMetrics()

	IncrAccountsConstrainRefusals()

	if got := GetMetrics()[MetricAccountsConstrainRefusals]; got != 1 {
		t.Fatalf("GetMetrics()[%q] = %d, want 1", MetricAccountsConstrainRefusals, got)
	}
	out := FormatMetrics()
	wantLine := MetricAccountsConstrainRefusals + " 1\n"
	if !strings.Contains(out, wantLine) {
		t.Fatalf("FormatMetrics() missing %q", wantLine)
	}
}
