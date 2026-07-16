package engine

// metrics_fit_score_test.go: Phase 6 fitness-function tests for the three new
// hunt fit-scoring metrics:
//
//  1. hunt_fit_score histogram (ObserveHuntFitScore)
//  2. hunt_score_filtered_total counter (IncrHuntScoreFiltered)
//  3. hunt_score_llm_total counter (IncrHuntScoreLLM)
//
// RED-on-revert:
//   - Remove MetricHuntFitScore const → const test fails.
//   - Remove IncrHuntScoreFiltered → filter counter never lands → test fails.
//   - Remove IncrHuntScoreLLM → LLM counter never lands → test fails.
//   - Remove a label from validHuntScoreFilterStages/validHuntScoreLLMResults
//     → the exact-set test fails.
//   - Add an unknown label → the exact-set test fails.
//   - Call IncrHuntScoreFiltered("bogus") and it lands → cardinality test fails.

import (
	"testing"

	kitmetrics "github.com/anatolykoptev/go-kit/metrics"
)

// TestMetricHuntFitScore_ConstDefined verifies the metric constant name.
func TestMetricHuntFitScore_ConstDefined(t *testing.T) {
	if MetricHuntFitScore != "hunt_fit_score" {
		t.Errorf("MetricHuntFitScore = %q, want %q", MetricHuntFitScore, "hunt_fit_score")
	}
}

// TestMetricHuntScoreFiltered_ConstDefined verifies the filtered counter constant.
func TestMetricHuntScoreFiltered_ConstDefined(t *testing.T) {
	if MetricHuntScoreFiltered != "hunt_score_filtered_total" {
		t.Errorf("MetricHuntScoreFiltered = %q, want %q", MetricHuntScoreFiltered, "hunt_score_filtered_total")
	}
}

// TestMetricHuntScoreLLM_ConstDefined verifies the LLM result counter constant.
func TestMetricHuntScoreLLM_ConstDefined(t *testing.T) {
	if MetricHuntScoreLLM != "hunt_score_llm_total" {
		t.Errorf("MetricHuntScoreLLM = %q, want %q", MetricHuntScoreLLM, "hunt_score_llm_total")
	}
}

// TestIncrHuntScoreFiltered_LabelBounded verifies that:
//   - the three valid stages {"recency","jaccard","quality"} land in the registry
//   - an arbitrary stage is dropped (no cardinality leak)
func TestIncrHuntScoreFiltered_LabelBounded(t *testing.T) {
	orig := reg
	t.Cleanup(func() { reg = orig })
	reg = kitmetrics.NewRegistry()

	IncrHuntScoreFiltered("recency")
	IncrHuntScoreFiltered("jaccard")
	IncrHuntScoreFiltered("quality")
	IncrHuntScoreFiltered("bogus") // must be dropped

	snap := reg.Snapshot()
	for _, stage := range []string{"recency", "jaccard", "quality"} {
		key := MetricHuntScoreFiltered + "{stage=" + stage + "}"
		if snap[key] != 1 {
			t.Errorf("%s = %d, want 1", key, snap[key])
		}
	}
	if snap[MetricHuntScoreFiltered+"{stage=bogus}"] != 0 {
		t.Errorf("unknown stage 'bogus' leaked into metrics (cardinality risk)")
	}
}

// TestValidHuntScoreFilterStages_ExactSet asserts the bounded stage set is
// exactly {"recency","jaccard","quality"} — no additions or removals without updating tests.
func TestValidHuntScoreFilterStages_ExactSet(t *testing.T) {
	want := map[string]bool{"recency": true, "jaccard": true, "quality": true}
	for v := range want {
		if !validHuntScoreFilterStages[v] {
			t.Errorf("filter stage %q missing from validHuntScoreFilterStages", v)
		}
	}
	for v := range validHuntScoreFilterStages {
		if !want[v] {
			t.Errorf("unexpected filter stage %q in validHuntScoreFilterStages", v)
		}
	}
}

// TestIncrHuntScoreLLM_LabelBounded verifies that:
//   - all four valid results {"ok","enum_clamp","parse_fail","llm_error"} land
//   - an arbitrary result is dropped
func TestIncrHuntScoreLLM_LabelBounded(t *testing.T) {
	orig := reg
	t.Cleanup(func() { reg = orig })
	reg = kitmetrics.NewRegistry()

	for _, r := range []string{"ok", "enum_clamp", "parse_fail", "llm_error"} {
		IncrHuntScoreLLM(r)
	}
	IncrHuntScoreLLM("unknown") // must be dropped

	snap := reg.Snapshot()
	for _, r := range []string{"ok", "enum_clamp", "parse_fail", "llm_error"} {
		key := MetricHuntScoreLLM + "{result=" + r + "}"
		if snap[key] != 1 {
			t.Errorf("%s = %d, want 1", key, snap[key])
		}
	}
	if snap[MetricHuntScoreLLM+"{result=unknown}"] != 0 {
		t.Errorf("unknown result 'unknown' leaked into metrics (cardinality risk)")
	}
}

// TestValidHuntScoreLLMResults_ExactSet asserts the bounded result set is
// exactly {"ok","enum_clamp","parse_fail","llm_error"}.
func TestValidHuntScoreLLMResults_ExactSet(t *testing.T) {
	want := map[string]bool{
		"ok":         true,
		"enum_clamp": true,
		"parse_fail": true,
		"llm_error":  true,
	}
	for v := range want {
		if !validHuntScoreLLMResults[v] {
			t.Errorf("LLM result %q missing from validHuntScoreLLMResults", v)
		}
	}
	for v := range validHuntScoreLLMResults {
		if !want[v] {
			t.Errorf("unexpected LLM result %q in validHuntScoreLLMResults", v)
		}
	}
}

// TestObserveHuntFitScore_DoesNotPanic verifies the histogram observe path does
// not panic when called with a fresh in-memory registry (no Prometheus bridge).
func TestObserveHuntFitScore_DoesNotPanic(t *testing.T) {
	orig := reg
	t.Cleanup(func() { reg = orig })
	reg = kitmetrics.NewRegistry()

	// Should not panic; histogram support is in the go-kit registry.
	ObserveHuntFitScore(80)
	ObserveHuntFitScore(0)
	ObserveHuntFitScore(100)
}
