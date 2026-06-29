package adminui

import (
	"strings"
	"testing"

	"github.com/anatolykoptev/go_job/internal/hunt"
)

// TestPipelineOptgroupHTML_Structure verifies the pipeline optgroup label is present.
// Red-on-revert: removing the Pipeline optgroup → this fails.
func TestPipelineOptgroupHTML_Structure(t *testing.T) {
	got := pipelineOptgroupHTML("applied")
	if !strings.Contains(got, `<optgroup label="Pipeline">`) {
		t.Errorf("pipelineOptgroupHTML: want Pipeline optgroup, got:\n%s", got)
	}
}

// TestPipelineOptgroupHTML_NoPipelineStagesAbsent verifies no triage values appear.
func TestPipelineOptgroupHTML_NoTriageValues(t *testing.T) {
	got := pipelineOptgroupHTML("")
	for _, s := range hunt.TriageStages {
		if strings.Contains(got, `value="`+s+`"`) {
			t.Errorf("pipelineOptgroupHTML: triage value %q must not appear in pipeline dropdown", s)
		}
	}
}

// TestPipelineOptgroupHTML_AllPipelineStagesPresent verifies all PipelineStages are rendered.
func TestPipelineOptgroupHTML_AllPipelineStagesPresent(t *testing.T) {
	got := pipelineOptgroupHTML("")
	for _, s := range hunt.PipelineStages {
		if !strings.Contains(got, `value="`+s+`"`) {
			t.Errorf("pipelineOptgroupHTML: PipelineStage %q not found in output", s)
		}
	}
}

// TestPipelineOptgroupHTML_ClearOptionPresent verifies "— clear —" appears when a
// stage is active (so operator can remove job from pipeline).
// Red-on-revert: removing the clear option → operator cannot blank the pipeline stage.
func TestPipelineOptgroupHTML_ClearOptionPresent(t *testing.T) {
	got := pipelineOptgroupHTML(hunt.StageApplied)
	if !strings.Contains(got, `<option value="">— clear —</option>`) {
		t.Errorf("pipelineOptgroupHTML(applied): want '— clear —' option with value=\"\", got:\n%s", got)
	}
}

// TestPipelineOptgroupHTML_ClearOptionAbsentWhenEmpty verifies "— clear —" is
// absent when no stage is set (nothing to clear).
func TestPipelineOptgroupHTML_ClearOptionAbsentWhenEmpty(t *testing.T) {
	got := pipelineOptgroupHTML("")
	if strings.Contains(got, `— clear —`) {
		t.Errorf("pipelineOptgroupHTML(\"\"): '— clear —' must not appear when no stage is set, got:\n%s", got)
	}
}

// TestPipelineOptgroupHTML_SelectedCorrect verifies the current pipeline stage is selected.
func TestPipelineOptgroupHTML_SelectedCorrect(t *testing.T) {
	for _, stage := range hunt.PipelineStages {
		got := pipelineOptgroupHTML(stage)
		want := `value="` + stage + `"` + attrSelected
		if !strings.Contains(got, want) {
			t.Errorf("pipelineOptgroupHTML(%q): want %q in output", stage, want)
		}
	}
}

// TestTriageSelectOptionsHTML_AllTriageStagesPresent verifies all TriageStages are rendered.
func TestTriageSelectOptionsHTML_AllTriageStagesPresent(t *testing.T) {
	got := triageSelectOptionsHTML("")
	for _, s := range hunt.TriageStages {
		if !strings.Contains(got, `value="`+s+`"`) {
			t.Errorf("triageSelectOptionsHTML: TriageStage %q not found in output", s)
		}
	}
}

// TestTriageSelectOptionsHTML_NoPipelineValues verifies no pipeline values appear.
func TestTriageSelectOptionsHTML_NoPipelineValues(t *testing.T) {
	got := triageSelectOptionsHTML("")
	for _, s := range hunt.PipelineStages {
		if strings.Contains(got, `value="`+s+`"`) {
			t.Errorf("triageSelectOptionsHTML: pipeline value %q must not appear in triage select", s)
		}
	}
}

// TestTriageSelectOptionsHTML_ClearOptionAlwaysPresent verifies blank "— none —"
// is always present so operator can clear the triage signal.
// Red-on-revert: removing the blank option → triage can never be cleared.
func TestTriageSelectOptionsHTML_ClearOptionAlwaysPresent(t *testing.T) {
	for _, cur := range append(hunt.TriageStages, "") {
		got := triageSelectOptionsHTML(cur)
		if !strings.Contains(got, `<option value=""`) {
			t.Errorf("triageSelectOptionsHTML(%q): blank clear option must always be present", cur)
		}
	}
}

// TestStageDropdownHTML_CSRFFieldPresent verifies stageDropdownHTML output
// contains the CSRF hidden field — required invariant from the stage handler.
func TestStageDropdownHTML_CSRFFieldPresent(t *testing.T) {
	got := stageDropdownHTML(42, "applied", "test-csrf-tok")
	if !strings.Contains(got, `name="_csrf"`) {
		t.Errorf("stageDropdownHTML: missing name=\"_csrf\" in output")
	}
	if !strings.Contains(got, `value="test-csrf-tok"`) {
		t.Errorf("stageDropdownHTML: missing CSRF token value in output")
	}
}

// TestStageDropdownHTML_PipelineOnly verifies that stageDropdownHTML (jobs-table
// inline dropdown) contains only the Pipeline optgroup, NOT a Triage optgroup.
// After migration 012: the stage dropdown controls only the pipeline axis.
//
// Red-on-revert: if stageDropdownHTML were restored to use a combined optgroup
// containing triage values, the triage filter bar would show editable pipeline
// values in the wrong form.
func TestStageDropdownHTML_PipelineOnly(t *testing.T) {
	got := stageDropdownHTML(1, "", "tok")
	if !strings.Contains(got, `<optgroup label="Pipeline">`) {
		t.Errorf("stageDropdownHTML: want Pipeline optgroup in output, got:\n%s", got)
	}
	if strings.Contains(got, `<optgroup label="Triage">`) {
		t.Errorf("stageDropdownHTML: Triage optgroup must NOT appear in the pipeline-only jobs dropdown")
	}
}
