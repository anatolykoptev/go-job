package engine

import "testing"

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
