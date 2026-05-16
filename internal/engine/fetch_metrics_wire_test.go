package engine

// fetch_metrics_wire_test.go asserts that the WithMetrics call site exists
// in the fetcher init path and that NewPromMetrics registers metrics on the
// provided registry without error.

import (
	"strings"
	"testing"

	"github.com/anatolykoptev/go-engine/fetch"
	"github.com/prometheus/client_golang/prometheus"
)

// TestFetchMetricsWire verifies that fetch.NewPromMetrics registers all
// go_engine_fetch_* counters/gauges on a fresh registry without error and
// that the resulting value is accepted by fetch.WithMetrics.
func TestFetchMetricsWire(t *testing.T) {
	reg := prometheus.NewRegistry()

	pm, err := fetch.NewPromMetrics(reg)
	if err != nil {
		t.Fatalf("NewPromMetrics: unexpected error: %v", err)
	}

	// WithMetrics must accept the value without panic.
	opts := []fetch.Option{fetch.WithMetrics(pm)}
	f := fetch.New(opts...)
	if f == nil {
		t.Fatal("fetch.New returned nil")
	}

	// Use Describe to verify all four metric families are registered.
	// Gather() skips zero-observation counters; Describe() is authoritative.
	ch := make(chan *prometheus.Desc, 16)
	reg.Describe(ch)
	close(ch)

	want := map[string]bool{
		"go_engine_fetch_tier_total":               false,
		"go_engine_fetch_block_signal_total":       false,
		"go_engine_fetch_proxy_escalations_total":  false,
		"go_engine_fetch_direct_block_cache_hosts": false,
	}
	for d := range ch {
		// Desc.String() contains the fully-qualified name as fqName="...".
		s := d.String()
		for name := range want {
			if strings.Contains(s, name) {
				want[name] = true
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("metric %q not found in registry after NewPromMetrics", name)
		}
	}
}

// TestFetchMetricsDuplicateRegistration verifies that a second call to
// NewPromMetrics on the same registry returns an error (prometheus already-registered
// guard), matching expected behaviour in prometheus.DefaultRegisterer.
func TestFetchMetricsDuplicateRegistration(t *testing.T) {
	reg := prometheus.NewRegistry()

	if _, err := fetch.NewPromMetrics(reg); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	if _, err := fetch.NewPromMetrics(reg); err == nil {
		t.Fatal("second registration on same registry: expected error, got nil")
	}
}
