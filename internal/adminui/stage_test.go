package adminui

import (
	"net/url"
	"strings"
	"testing"

	"github.com/anatolykoptev/go_job/internal/hunt"
)

// TestStageDropdownHTML_NoCurrentStage verifies that when no stage is set (empty
// string), the placeholder option ("— stage —") is selected and the form action
// points at /admin/jobs/{id}/stage.
// Red-on-revert: removing stageDropdownHTML → compile error.
func TestStageDropdownHTML_NoCurrentStage(t *testing.T) {
	got := stageDropdownHTML(42, "", "test-tok")
	for _, want := range []string{
		`action="/admin/jobs/42/stage"`,
		`method="POST"`,
		`name="stage"`,
		`name="_csrf"`,
		`value="test-tok"`,
		`aria-label="Pipeline stage"`,
		// Placeholder option: disabled, hidden, and selected when stage=="".
		`value="" disabled hidden selected`,
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
	// Placeholder option must NOT be selected (disabled hidden, no selected attr).
	if strings.Contains(got, `value="" disabled hidden selected`) {
		t.Errorf("stageDropdownHTML(7, \"applied\", ...): placeholder must not be selected when stage is set")
	}
}

// TestStageDropdownHTML_AllStagesPresent verifies all stages from hunt.AllStages
// appear as options in the dropdown.
// Red-on-revert: removing a stage from hunt.AllStages → this assertion fails.
func TestStageDropdownHTML_AllStagesPresent(t *testing.T) {
	got := stageDropdownHTML(1, "", "tok")
	for _, stage := range hunt.AllStages {
		if !strings.Contains(got, `value="`+stage+`"`) {
			t.Errorf("stageDropdownHTML: want option value=%q in output", stage)
		}
	}
}

// TestStageDropdownHTML_MalformedMarkup guards against a stray `>>` (same guard
// as the star toggle malformed-markup test).
// Red-on-revert: introducing a bare `>` in the HTML → this assertion fails.
func TestStageDropdownHTML_MalformedMarkup(t *testing.T) {
	got := stageDropdownHTML(99, "interview", "csrf123")
	if strings.Contains(got, ">>") {
		t.Errorf("stageDropdownHTML: malformed markup, contains \">>\":\n%s", got)
	}
}

// TestJobsSpec_StageColumn verifies the Stage column appears in jobsSpec at index 2
// (right after star at index 1), uses colKeyStage key, and is not confused with
// the job-posting-status column.
// Red-on-revert: removing the Stage column → index-out-of-bounds or key mismatch.
func TestJobsSpec_StageColumn(t *testing.T) {
	cols := jobsSpec.Columns
	if len(cols) < 3 {
		t.Fatalf("jobsSpec.Columns: want at least 3 columns, got %d", len(cols))
	}
	// Index 0: Title (Href-linked).
	if cols[0].Key != colKeyTitle {
		t.Errorf("cols[0]: want key=%q, got %q", colKeyTitle, cols[0].Key)
	}
	// Index 1: Star.
	if cols[1].Key != colKeyStar {
		t.Errorf("cols[1]: want key=%q, got %q", colKeyStar, cols[1].Key)
	}
	// Index 2: Stage dropdown (pipeline stage).
	if cols[2].Key != colKeyStage {
		t.Errorf("cols[2]: want key=%q (stage dropdown), got %q", colKeyStage, cols[2].Key)
	}
	if cols[2].Label != "Stage" {
		t.Errorf("cols[2]: want label=%q, got %q", "Stage", cols[2].Label)
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

// TestJobsFilter_StageFilter verifies the stage filter is wired in jobsFilter
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

// TestStageEnumSync verifies that validHuntStages, allHuntStageValues, and the
// stage options rendered by stageDropdownHTML stay in sync with hunt.AllStages.
// Red-on-revert: adding a stage to hunt.AllStages without propagating → this fails.
func TestStageEnumSync(t *testing.T) {
	t.Run("validHuntStages", func(t *testing.T) {
		// Every entry in hunt.AllStages must be in validHuntStages.
		for _, s := range hunt.AllStages {
			if !validHuntStages[s] {
				t.Errorf("validHuntStages: missing stage %q (present in hunt.AllStages)", s)
			}
		}
		// validHuntStages must not contain extras beyond hunt.AllStages.
		canonical := make(map[string]bool, len(hunt.AllStages))
		for _, s := range hunt.AllStages {
			canonical[s] = true
		}
		for s := range validHuntStages {
			if !canonical[s] {
				t.Errorf("validHuntStages: extra stage %q not in hunt.AllStages", s)
			}
		}
	})

	t.Run("allHuntStageValues", func(t *testing.T) {
		if len(allHuntStageValues) != len(hunt.AllStages) {
			t.Errorf("allHuntStageValues: len=%d, want %d (hunt.AllStages)", len(allHuntStageValues), len(hunt.AllStages))
		}
		for i, s := range hunt.AllStages {
			if i >= len(allHuntStageValues) {
				break
			}
			if allHuntStageValues[i] != s {
				t.Errorf("allHuntStageValues[%d]=%q, want %q", i, allHuntStageValues[i], s)
			}
		}
	})

	t.Run("stageDropdownOptions", func(t *testing.T) {
		got := stageDropdownHTML(1, "", "tok")
		for _, s := range hunt.AllStages {
			if !strings.Contains(got, `value="`+s+`"`) {
				t.Errorf("stageDropdownHTML: missing option for stage %q (present in hunt.AllStages)", s)
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

// TestStageDropdownHTML_PlaceholderDisabledHidden verifies the placeholder option
// is marked disabled and hidden so users cannot re-select "no stage" after setting one,
// while still allowing the browser to display it when stage=="".
func TestStageDropdownHTML_PlaceholderDisabledHidden(t *testing.T) {
	// When stage is unset, placeholder must be disabled hidden AND selected.
	gotEmpty := stageDropdownHTML(5, "", "tok")
	if !strings.Contains(gotEmpty, `value="" disabled hidden selected`) {
		t.Errorf("stageDropdownHTML (no stage): placeholder must be disabled hidden selected, got:\n%s", gotEmpty)
	}
	// When a stage is set, placeholder must be disabled hidden but NOT selected.
	gotSet := stageDropdownHTML(5, "applied", "tok")
	if !strings.Contains(gotSet, `value="" disabled hidden`) {
		t.Errorf("stageDropdownHTML (applied): placeholder must be disabled hidden, got:\n%s", gotSet)
	}
	if strings.Contains(gotSet, `value="" disabled hidden selected`) {
		t.Errorf("stageDropdownHTML (applied): placeholder must not be selected when stage is set")
	}
}
