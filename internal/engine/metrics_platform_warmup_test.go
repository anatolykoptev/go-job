package engine

import (
	"testing"

	kitmetrics "github.com/anatolykoptev/go-kit/metrics"
)

// TestWarmAlertBoundedMetrics_PreRegistersFullMatrix swaps the package
// registry for a fresh in-memory one (no prom bridge → no DefaultRegisterer
// collision) and asserts that warmAlertBoundedMetrics seeds every
// platform×outcome key backing GojobSourceParseFail / GojobSourceNoKey, plus
// every discovery-source key backing GojobDelegationFallback (see
// ~/deploy/server-config/config/prometheus/alerts-go-job.yml).
//
// Presence, not just value, is what's asserted: a MISSING key is exactly the
// bug this warm-up fixes (increase() cannot see a series' first real
// increment when the series doesn't exist yet); value 0 on a PRESENT key is
// the correct warmed state. Revert warmAlertBoundedMetrics to a no-op and
// every assertion below goes red — the map lookup on the fresh registry
// returns "not found", not a zero.
func TestWarmAlertBoundedMetrics_PreRegistersFullMatrix(t *testing.T) {
	orig := reg
	t.Cleanup(func() { reg = orig })
	reg = kitmetrics.NewRegistry()

	warmAlertBoundedMetrics()

	snap := reg.Snapshot()

	for p := range validPlatforms {
		for oc := range validPlatformOutcomes {
			key := MetricPlatformResults + "{platform=" + p + ",outcome=" + oc + "}"
			if v, ok := snap[key]; !ok || v != 0 {
				t.Errorf("platform_results_total series %q missing/non-zero after warm-up (present=%v value=%d)", key, ok, v)
			}
		}
	}

	// The two alert-critical outcomes explicitly, matching alerts-go-job.yml
	// (GojobSourceParseFail / GojobSourceNoKey fire on any platform).
	for _, key := range []string{
		MetricPlatformResults + "{platform=greenhouse,outcome=" + outcomeParseFail + "}",
		MetricPlatformResults + "{platform=greenhouse,outcome=" + outcomeNoKey + "}",
	} {
		if _, ok := snap[key]; !ok {
			t.Errorf("alert-backing series %q not pre-registered", key)
		}
	}

	for src := range validDiscoverySources {
		key := MetricHuntDiscoverySource + "{source=" + src + "}"
		if v, ok := snap[key]; !ok || v != 0 {
			t.Errorf("hunt_discovery_source_total series %q missing/non-zero after warm-up (present=%v value=%d)", key, ok, v)
		}
	}
}

// TestInit_WarmsPlatformResultsMatrix is an integration-level check that
// Init() itself performs the warm-up (not just that the helper works in
// isolation). It drives the real Init() path and inspects the resulting
// registry's local Snapshot — which only contains the labeled key if Init()
// actually invoked warmAlertBoundedMetrics before returning.
func TestInit_WarmsPlatformResultsMatrix(t *testing.T) {
	Init(Config{})

	snap := reg.Snapshot()
	key := MetricPlatformResults + "{platform=linkedin,outcome=" + outcomeParseFail + "}"
	if v, ok := snap[key]; !ok || v != 0 {
		t.Errorf("Init() did not warm %q (present=%v value=%d) — warmAlertBoundedMetrics not wired into Init", key, ok, v)
	}
}
