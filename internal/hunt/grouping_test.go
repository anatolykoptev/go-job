package hunt

import "testing"

// TestStageGrouping verifies the domain grouping primitives are consistent:
//   - union of TriageStages ++ PipelineStages == AllStages (order-preserving)
//   - intersection is empty (no stage belongs to both groups)
//   - AllStages == TriageStages ++ PipelineStages byte-for-byte
func TestStageGrouping(t *testing.T) {
	t.Run("AllStages_is_triage_concat_pipeline", func(t *testing.T) {
		want := append(append([]string{}, TriageStages...), PipelineStages...)
		if len(AllStages) != len(want) {
			t.Fatalf("AllStages len=%d, want %d (len(TriageStages)+len(PipelineStages))", len(AllStages), len(want))
		}
		for i, s := range want {
			if AllStages[i] != s {
				t.Errorf("AllStages[%d]=%q, want %q", i, AllStages[i], s)
			}
		}
	})

	t.Run("intersection_empty", func(t *testing.T) {
		triage := make(map[string]bool, len(TriageStages))
		for _, s := range TriageStages {
			triage[s] = true
		}
		for _, s := range PipelineStages {
			if triage[s] {
				t.Errorf("stage %q appears in both TriageStages and PipelineStages", s)
			}
		}
	})

	t.Run("TriageStages_coverage", func(t *testing.T) {
		// Every constant the comment documents must appear exactly once.
		want := []string{StageNew, StageInteresting, StageSaved, StageDiscarded, StageClaimed}
		if len(TriageStages) != len(want) {
			t.Fatalf("TriageStages len=%d, want %d", len(TriageStages), len(want))
		}
		for i, s := range want {
			if TriageStages[i] != s {
				t.Errorf("TriageStages[%d]=%q, want %q", i, TriageStages[i], s)
			}
		}
	})

	t.Run("PipelineStages_coverage", func(t *testing.T) {
		want := []string{StageApplied, StageInterview, StageOffer, StageRejected}
		if len(PipelineStages) != len(want) {
			t.Fatalf("PipelineStages len=%d, want %d", len(PipelineStages), len(want))
		}
		for i, s := range want {
			if PipelineStages[i] != s {
				t.Errorf("PipelineStages[%d]=%q, want %q", i, PipelineStages[i], s)
			}
		}
	})
}
