package adminui

import (
	"strings"
	"testing"

	"github.com/anatolykoptev/go_job/internal/hunt"
)

// TestStageOptgroupHTML_Structure verifies both optgroup labels are present in output.
func TestStageOptgroupHTML_Structure(t *testing.T) {
	got := stageOptgroupHTML("new")
	for _, want := range []string{
		`<optgroup label="Triage">`,
		`<optgroup label="Pipeline">`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stageOptgroupHTML: want %q in output, got:\n%s", want, got)
		}
	}
}

// TestStageOptgroupHTML_TriageMembership verifies all TriageStages are in the
// Triage optgroup and none appear in the Pipeline optgroup.
func TestStageOptgroupHTML_TriageMembership(t *testing.T) {
	got := stageOptgroupHTML("")
	// Locate the two optgroup blocks by splitting on Pipeline optgroup.
	triagePart, pipelinePart, found := strings.Cut(got, `<optgroup label="Pipeline">`)
	if !found {
		t.Fatal("stageOptgroupHTML: missing Pipeline optgroup")
	}
	for _, s := range hunt.TriageStages {
		if !strings.Contains(triagePart, `value="`+s+`"`) {
			t.Errorf("stageOptgroupHTML: TriageStage %q not found in Triage block", s)
		}
		if strings.Contains(pipelinePart, `value="`+s+`"`) {
			t.Errorf("stageOptgroupHTML: TriageStage %q appears in Pipeline block", s)
		}
	}
}

// TestStageOptgroupHTML_PipelineMembership verifies all PipelineStages are in the
// Pipeline optgroup and none appear in the Triage optgroup.
func TestStageOptgroupHTML_PipelineMembership(t *testing.T) {
	got := stageOptgroupHTML("")
	triagePart, pipelinePart, found := strings.Cut(got, `<optgroup label="Pipeline">`)
	if !found {
		t.Fatal("stageOptgroupHTML: missing Pipeline optgroup")
	}
	for _, s := range hunt.PipelineStages {
		if !strings.Contains(pipelinePart, `value="`+s+`"`) {
			t.Errorf("stageOptgroupHTML: PipelineStage %q not found in Pipeline block", s)
		}
		if strings.Contains(triagePart, `value="`+s+`"`) {
			t.Errorf("stageOptgroupHTML: PipelineStage %q appears in Triage block", s)
		}
	}
}

// TestStageOptgroupHTML_SelectedCorrect verifies the currentStage option is marked selected.
func TestStageOptgroupHTML_SelectedCorrect(t *testing.T) {
	for _, stage := range hunt.AllStages {
		got := stageOptgroupHTML(stage)
		want := `value="` + stage + `" selected`
		if !strings.Contains(got, want) {
			t.Errorf("stageOptgroupHTML(%q): want %q in output", stage, want)
		}
	}
}

// TestStageOptgroupHTML_EmptyPlaceholder verifies the "— stage —" placeholder is
// rendered as disabled+hidden+selected when currentStage=="".
func TestStageOptgroupHTML_EmptyPlaceholder(t *testing.T) {
	got := stageOptgroupHTML("")
	if !strings.Contains(got, `value="" disabled hidden selected`) {
		t.Errorf("stageOptgroupHTML(\"\"): want disabled hidden selected placeholder, got:\n%s", got)
	}
}

// TestStageOptgroupHTML_OutOfEnumSentinel verifies the sentinel option is rendered
// for a value not in AllStages.
func TestStageOptgroupHTML_OutOfEnumSentinel(t *testing.T) {
	got := stageOptgroupHTML("unknown-legacy-stage")
	if !strings.Contains(got, `disabled hidden selected`) {
		t.Errorf("stageOptgroupHTML(unknown): want sentinel disabled hidden selected, got:\n%s", got)
	}
	// Sentinel text must be escaped — user-supplied stage value never raw in HTML.
	if !strings.Contains(got, `current: unknown-legacy-stage`) {
		t.Errorf("stageOptgroupHTML(unknown): want sentinel 'current: ...' in output, got:\n%s", got)
	}
}

// TestStageOptgroupHTML_OutOfEnumEscaping verifies that an out-of-enum
// currentStage containing HTML metacharacters is properly escaped so the
// html.EscapeString invariant that backs the //nolint:gosec G203 annotation
// cannot silently regress.
func TestStageOptgroupHTML_OutOfEnumEscaping(t *testing.T) {
	payload := `<script>alert(1)</script>`
	got := stageOptgroupHTML(payload)
	if strings.Contains(got, payload) {
		t.Errorf("stageOptgroupHTML: raw %q appeared unescaped in output", payload)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("stageOptgroupHTML: expected &lt;script&gt; escaped form in output, got:\n%s", got)
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
// Triage is managed by a separate /triage form on the job-detail page.
//
// Red-on-revert: reverting stageDropdownHTML to use stageOptgroupHTML (combined) →
// Triage optgroup would appear in the jobs-table dropdown, which is incorrect.
func TestStageDropdownHTML_PipelineOnly(t *testing.T) {
	got := stageDropdownHTML(1, "", "tok")
	// Pipeline optgroup must be present.
	if !strings.Contains(got, `<optgroup label="Pipeline">`) {
		t.Errorf("stageDropdownHTML: want Pipeline optgroup in output, got:\n%s", got)
	}
	// Triage optgroup must NOT be present — it belongs to the separate /triage form.
	if strings.Contains(got, `<optgroup label="Triage">`) {
		t.Errorf("stageDropdownHTML: Triage optgroup must NOT appear in the pipeline-only jobs dropdown")
	}
}
