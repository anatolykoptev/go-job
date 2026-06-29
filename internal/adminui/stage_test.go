package adminui

import (
	"net/url"
	"strings"
	"testing"

	"github.com/anatolykoptev/go_job/internal/hunt"
)

// TestStageDropdownHTML_NoCurrentStage verifies that when no pipeline stage is set
// (empty string), the "— pipeline —" option is rendered selected, and the form action
// points at /admin/jobs/{id}/stage.
// Red-on-revert: removing pipelineOptgroupHTML → compile error.
func TestStageDropdownHTML_NoCurrentStage(t *testing.T) {
	got := stageDropdownHTML(42, "", "test-tok")
	for _, want := range []string{
		`action="/admin/jobs/42/stage"`,
		`method="POST"`,
		`name="stage"`,
		`name="_csrf"`,
		`value="test-tok"`,
		`aria-label="My pipeline"`,
		// Pipeline-only: shows "— pipeline —" placeholder when no stage is set.
		`value="" selected`,
		// Explicit submit button (WCAG 3.2.2 — no onchange auto-submit).
		`type="submit"`,
		`aria-label="Save stage"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stageDropdownHTML(42, \"\", ...): want %q in output, got:\n%s", want, got)
		}
	}
	// onchange auto-submit must NOT be present (WCAG 3.2.2).
	if strings.Contains(got, `onchange="this.form.submit()"`) {
		t.Errorf("stageDropdownHTML: onchange auto-submit violates WCAG 3.2.2 — must use explicit submit button")
	}
}

// TestStageDropdownHTML_AppliedSelected verifies that the "applied" option is
// marked selected when currentStage="applied".
// Red-on-revert: removing the selected-attribute logic → wrong option selected.
func TestStageDropdownHTML_AppliedSelected(t *testing.T) {
	got := stageDropdownHTML(7, "applied", "tok")
	// "applied" option must be selected.
	if !strings.Contains(got, `value="applied" selected`) {
		t.Errorf("stageDropdownHTML(7, \"applied\", ...): want applied option selected, got:\n%s", got)
	}
}

// TestStageDropdownHTML_PipelineStagesPresent verifies all pipeline stages from
// hunt.PipelineStages appear as options in the dropdown. Triage values must NOT appear.
//
// Red-on-revert: removing a pipeline stage from hunt.PipelineStages → assertion fails.
// Migration 012: triage values are now managed via a separate /triage form.
func TestStageDropdownHTML_PipelineStagesPresent(t *testing.T) {
	got := stageDropdownHTML(1, "", "tok")
	for _, stage := range hunt.PipelineStages {
		if !strings.Contains(got, `value="`+stage+`"`) {
			t.Errorf("stageDropdownHTML: want pipeline option value=%q in output", stage)
		}
	}
	// Triage values must NOT appear in the pipeline dropdown.
	for _, triage := range hunt.TriageStages {
		// We look for a standalone option value, not just the string (e.g. "applied" contains "d").
		if strings.Contains(got, `value="`+triage+`"`) {
			t.Errorf("stageDropdownHTML: triage value %q must NOT appear in pipeline dropdown", triage)
		}
	}
}

// TestStageDropdownHTML_MalformedMarkup guards against a stray `>>` in the output.
// Red-on-revert: introducing a bare `>` in the HTML → this assertion fails.
func TestStageDropdownHTML_MalformedMarkup(t *testing.T) {
	got := stageDropdownHTML(99, "interview", "csrf123")
	if strings.Contains(got, ">>") {
		t.Errorf("stageDropdownHTML: malformed markup, contains \">>\":\n%s", got)
	}
}

// TestJobsSpec_TriageAndStageColumns verifies the layout after migration 012:
//   - [2] Triage badge (read-only, colKeyTriage)
//   - [3] Stage dropdown (pipeline-only, colKeyStage)
//
// The status column (job posting open/closed) must still be present and separate.
// Red-on-revert: removing/reordering columns → key mismatch; triage filter
// filter would produce no visible output in the table.
func TestJobsSpec_TriageAndStageColumns(t *testing.T) {
	cols := jobsSpec.Columns
	if len(cols) < 5 {
		t.Fatalf("jobsSpec.Columns: want at least 5 columns, got %d", len(cols))
	}
	if cols[0].Key != colKeyTitle {
		t.Errorf("cols[0]: want key=%q, got %q", colKeyTitle, cols[0].Key)
	}
	if cols[1].Key != colKeyStar {
		t.Errorf("cols[1]: want key=%q, got %q", colKeyStar, cols[1].Key)
	}
	// Index 2: read-only Triage badge — makes the triage filter bar produce output.
	if cols[2].Key != colKeyTriage {
		t.Errorf("cols[2]: want key=%q (triage badge), got %q", colKeyTriage, cols[2].Key)
	}
	if cols[2].Label != "Triage" {
		t.Errorf("cols[2]: want label=%q, got %q", "Triage", cols[2].Label)
	}
	// Index 3: Stage dropdown (pipeline stage only).
	if cols[3].Key != colKeyStage {
		t.Errorf("cols[3]: want key=%q (stage dropdown), got %q", colKeyStage, cols[3].Key)
	}
	if cols[3].Label != "Stage" {
		t.Errorf("cols[3]: want label=%q, got %q", "Stage", cols[3].Label)
	}
	// The status column (job posting open/closed) must still be present and separate.
	found := false
	for _, c := range cols {
		if c.Key == colStatus {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("jobsSpec.Columns: status column (key=%q) must still be present", colStatus)
	}
}

// TestJobsFilter_StageFilter verifies the pipeline-stage filter is wired in jobsFilter
// and binds the correct SQL expression for the joined r.stage column.
// Red-on-revert: removing the stage filter → stage=applied URL param has no effect.
func TestJobsFilter_StageFilter(t *testing.T) {
	vals := url.Values{"stage": {"applied"}}
	conds, args := jobsFilter.Where(vals, 1)
	if conds == "" {
		t.Error("jobsFilter: stage=applied should produce a WHERE condition, got empty")
	}
	if len(args) == 0 {
		t.Error("jobsFilter: stage=applied should produce at least one bind arg")
	}
	// The SQL condition must reference r.stage, not j.status.
	if !strings.Contains(conds, "r.stage") {
		t.Errorf("jobsFilter: stage filter conds must reference r.stage, got: %s", conds)
	}
}

// TestJobsFilter_TriageFilter verifies the triage filter is wired in jobsFilter
// and binds the correct SQL expression for the joined r.triage column.
// Red-on-revert: removing the triage filter → triage=saved URL param has no effect.
func TestJobsFilter_TriageFilter(t *testing.T) {
	vals := url.Values{"triage": {"saved"}}
	conds, args := jobsFilter.Where(vals, 1)
	if conds == "" {
		t.Error("jobsFilter: triage=saved should produce a WHERE condition, got empty")
	}
	if len(args) == 0 {
		t.Error("jobsFilter: triage=saved should produce at least one bind arg")
	}
	if !strings.Contains(conds, "r.triage") {
		t.Errorf("jobsFilter: triage filter conds must reference r.triage, got: %s", conds)
	}
}

// TestJobsFilter_StageInvalidDropped verifies an unknown stage value is silently
// dropped (safe-degrade — no error, no SQL injection path).
// Red-on-revert: removing Allowed from the stage filter → invalid value passes through.
func TestJobsFilter_StageInvalidDropped(t *testing.T) {
	vals := url.Values{"stage": {"hacked'; DROP TABLE hunt_jobs;--"}}
	conds, args := jobsFilter.Where(vals, 1)
	// Unknown stage is outside Allowed — filter must be dropped.
	if strings.Contains(conds, "r.stage") {
		t.Errorf("jobsFilter: invalid stage must be dropped, but conds=%q still references r.stage", conds)
	}
	if len(args) != 0 {
		t.Errorf("jobsFilter: invalid stage must produce zero bind args, got %d", len(args))
	}
}

// TestStageEnumSync verifies that validPipelineStages, validTriageStages, and the
// options rendered by stageDropdownHTML stay in sync with hunt.PipelineStages /
// hunt.TriageStages respectively.
//
// Red-on-revert: adding a stage to hunt.PipelineStages without propagating →
// the options sub-test fails.
func TestStageEnumSync(t *testing.T) {
	t.Run("validPipelineStages", func(t *testing.T) {
		for _, s := range hunt.PipelineStages {
			if !validPipelineStages[s] {
				t.Errorf("validPipelineStages: missing stage %q (present in hunt.PipelineStages)", s)
			}
		}
		canonical := make(map[string]bool, len(hunt.PipelineStages))
		for _, s := range hunt.PipelineStages {
			canonical[s] = true
		}
		for s := range validPipelineStages {
			if !canonical[s] {
				t.Errorf("validPipelineStages: extra stage %q not in hunt.PipelineStages", s)
			}
		}
	})

	t.Run("validTriageStages", func(t *testing.T) {
		for _, s := range hunt.TriageStages {
			if !validTriageStages[s] {
				t.Errorf("validTriageStages: missing stage %q (present in hunt.TriageStages)", s)
			}
		}
		canonical := make(map[string]bool, len(hunt.TriageStages))
		for _, s := range hunt.TriageStages {
			canonical[s] = true
		}
		for s := range validTriageStages {
			if !canonical[s] {
				t.Errorf("validTriageStages: extra stage %q not in hunt.TriageStages", s)
			}
		}
	})

	t.Run("stageDropdownOptions_pipeline_only", func(t *testing.T) {
		got := stageDropdownHTML(1, "", "tok")
		for _, s := range hunt.PipelineStages {
			if !strings.Contains(got, `value="`+s+`"`) {
				t.Errorf("stageDropdownHTML: missing option for pipeline stage %q", s)
			}
		}
		// Triage values must not be in the pipeline dropdown.
		for _, s := range hunt.TriageStages {
			if strings.Contains(got, `value="`+s+`"`) {
				t.Errorf("stageDropdownHTML: triage value %q must not appear in pipeline dropdown", s)
			}
		}
	})
}

// TestStageDropdownHTML_ShellTokens verifies the select style uses CSS custom
// properties defined in go-panel/shell (styles_templ.go) and not the stale
// undefined --input-bg or --text tokens that caused white-on-dark rendering.
// Red-on-revert: reverting to --input-bg/--text → this test fails.
func TestStageDropdownHTML_ShellTokens(t *testing.T) {
	got := stageDropdownHTML(1, "", "tok")
	// Must use defined shell tokens.
	for _, token := range []string{"--bg-elevated", "--text-primary", "--border"} {
		if !strings.Contains(got, token) {
			t.Errorf("stageDropdownHTML: must reference shell CSS token %q, not found in output:\n%s", token, got)
		}
	}
	// Must NOT use the undefined tokens that caused white-on-dark regression.
	for _, bad := range []string{"--input-bg", "var(--text,"} {
		if strings.Contains(got, bad) {
			t.Errorf("stageDropdownHTML: must not reference undefined token %q (causes white-on-dark), got:\n%s", bad, got)
		}
	}
}

// TestStageDropdownHTML_PlaceholderBehavior verifies placeholder option behaviour
// after migration 012 (pipeline-only dropdown uses "— pipeline —" / "— clear —").
func TestStageDropdownHTML_PlaceholderBehavior(t *testing.T) {
	// When stage is unset, "— pipeline —" placeholder must be selected.
	gotEmpty := stageDropdownHTML(5, "", "tok")
	if !strings.Contains(gotEmpty, `value="" selected`) {
		t.Errorf("stageDropdownHTML (no stage): pipeline placeholder must be selected, got:\n%s", gotEmpty)
	}

	// When a valid pipeline stage is set, the matching option must be selected.
	gotSet := stageDropdownHTML(5, "applied", "tok")
	if !strings.Contains(gotSet, `value="applied" selected`) {
		t.Errorf("stageDropdownHTML (applied): want applied option selected, got:\n%s", gotSet)
	}
}
