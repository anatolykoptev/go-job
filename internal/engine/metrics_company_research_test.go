package engine

import (
	"testing"

	kitmetrics "github.com/anatolykoptev/go-kit/metrics"
)

// TestMetricCompanyResearch_ConstDefined verifies the metric constant is the
// expected Prometheus-conventional name.
func TestMetricCompanyResearch_ConstDefined(t *testing.T) {
	if MetricCompanyResearch != "company_research_total" {
		t.Errorf("MetricCompanyResearch = %q, want %q", MetricCompanyResearch, "company_research_total")
	}
}

// TestIncrCompanyResearch_LabelBounded verifies the outcome label is bounded so
// an arbitrary string cannot blow up label cardinality. Unrecognised outcomes
// are silently dropped (validCompanyResearchOutcomes gate).
func TestIncrCompanyResearch_LabelBounded(t *testing.T) {
	valid := []string{"ok", "timeout", "error"}
	for _, oc := range valid {
		if !validCompanyResearchOutcomes[oc] {
			t.Errorf("outcome %q should be a valid company-research outcome", oc)
		}
	}
	invalid := []string{"", "OK", "deadline", "context canceled: dial tcp ...", "panic"}
	for _, oc := range invalid {
		if validCompanyResearchOutcomes[oc] {
			t.Errorf("outcome %q must NOT be valid (cardinality risk)", oc)
		}
	}
	// reg is nil in a bare test binary; IncrCompanyResearch must not panic
	// (reg.Incr is nil-safe) for any input, recognised or not.
	IncrCompanyResearch("timeout")
	IncrCompanyResearch("arbitrary-unbounded-string")
}

// TestIncrCompanyResearch_RegistryLands swaps the package registry for a fresh
// in-memory one (no prom bridge → no DefaultRegisterer collision) and asserts
// that a recognised outcome actually lands under the labelled key, and an
// unrecognised outcome is dropped (never reaches the registry). This covers the
// path that the nil-reg test above cannot — the real Incr → Snapshot round-trip.
func TestIncrCompanyResearch_RegistryLands(t *testing.T) {
	orig := reg
	t.Cleanup(func() { reg = orig })
	reg = kitmetrics.NewRegistry()

	IncrCompanyResearch("timeout")
	IncrCompanyResearch("timeout")
	IncrCompanyResearch("ok")
	IncrCompanyResearch("definitely-not-a-valid-outcome") // must be dropped

	snap := reg.Snapshot()

	wantTimeout := MetricCompanyResearch + "{outcome=timeout}"
	if snap[wantTimeout] != 2 {
		t.Errorf("%s = %d, want 2", wantTimeout, snap[wantTimeout])
	}
	wantOK := MetricCompanyResearch + "{outcome=ok}"
	if snap[wantOK] != 1 {
		t.Errorf("%s = %d, want 1", wantOK, snap[wantOK])
	}
	// The dropped outcome must not appear under any key.
	for k, v := range snap {
		if k != wantTimeout && k != wantOK && v != 0 {
			t.Errorf("unexpected metric key %q=%d (label cardinality leak?)", k, v)
		}
	}
}
