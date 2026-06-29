package adminui

import (
	"net/url"
	"strings"
	"testing"
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
		`onchange="this.form.submit()"`,
		`name="_csrf"`,
		`value="test-tok"`,
		`aria-label="Pipeline stage"`,
		// Placeholder option selected when stage=="".
		`value="" selected`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stageDropdownHTML(42, \"\", ...): want %q in output, got:\n%s", want, got)
		}
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
	// Placeholder option must NOT be selected.
	if strings.Contains(got, `value="" selected`) {
		t.Errorf("stageDropdownHTML(7, \"applied\", ...): placeholder must not be selected when stage is set")
	}
}

// TestStageDropdownHTML_AllStagesPresent verifies all 9 stage values appear as options.
// Red-on-revert: removing a stage from stageOptions → this assertion fails.
func TestStageDropdownHTML_AllStagesPresent(t *testing.T) {
	got := stageDropdownHTML(1, "", "tok")
	for _, stage := range []string{"new", "interesting", "saved", "discarded", "claimed", "applied", "interview", "offer", "rejected"} {
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
