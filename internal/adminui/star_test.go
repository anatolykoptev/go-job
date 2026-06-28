package adminui

import (
	"net/url"
	"strings"
	"testing"
)

// TestStarToggleHTML_Unshortlisted verifies the ☆ state: hollow star, correct
// action URL, CSRF token present.
// Red-on-revert: removing starToggleHTML or inverting the star glyph → fails.
func TestStarToggleHTML_Unshortlisted(t *testing.T) {
	got := starToggleHTML(42, false, "test-tok")
	for _, want := range []string{
		`action="/admin/jobs/42/shortlist"`,
		`method="POST"`,
		`☆`,
		`name="_csrf"`,
		`value="test-tok"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("starToggleHTML(42, false): want %q in output, got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "★") {
		t.Errorf("starToggleHTML(42, false): should not contain filled star ★")
	}
	// Guard against a malformed tag (e.g. a stray `">>` closing the form),
	// which renders a literal `>` in every table row. `>>` must never appear.
	if strings.Contains(got, ">>") {
		t.Errorf("starToggleHTML: malformed markup, contains \">>\":\n%s", got)
	}
}

// TestStarToggleHTML_Shortlisted verifies the ★ state: filled star, correct
// action URL.
// Red-on-revert: inverting the shortlisted branch → ★ missing in filled state.
func TestStarToggleHTML_Shortlisted(t *testing.T) {
	got := starToggleHTML(99, true, "another-tok")
	if !strings.Contains(got, "★") {
		t.Errorf("starToggleHTML(99, true): want ★ in output, got:\n%s", got)
	}
	if !strings.Contains(got, `action="/admin/jobs/99/shortlist"`) {
		t.Errorf("starToggleHTML(99, true): want correct action URL, got:\n%s", got)
	}
	if strings.Contains(got, "☆") {
		t.Errorf("starToggleHTML(99, true): should not contain hollow star ☆")
	}
}

// TestStarToggleHTML_CSRFNotInAction verifies the CSRF token value is in the
// hidden input, not in the action URL (no accidental leakage into the URL).
func TestStarToggleHTML_CSRFNotInAction(t *testing.T) {
	tok := "1735689600|ab8662f5deadbeef"
	got := starToggleHTML(1, false, tok)
	actionIdx := strings.Index(got, `action="`)
	inputIdx := strings.Index(got, `name="_csrf"`)
	if actionIdx < 0 || inputIdx < 0 {
		t.Fatalf("action or csrf input not found in: %s", got)
	}
	// Token must not appear before the csrf input (e.g., in the action URL).
	beforeCSRF := got[:inputIdx]
	if strings.Contains(beforeCSRF, tok) {
		t.Errorf("CSRF token leaked into action URL or before hidden input: %s", beforeCSRF)
	}
}

// TestJobsFilter_ShortlistedFilter verifies the shortlisted=true filter reaches
// the WHERE clause as a bind arg, not concatenated SQL.
// Red-on-revert: removing the shortlisted filter from jobsFilter → len(args)==0.
func TestJobsFilter_ShortlistedFilter(t *testing.T) {
	vals := url.Values{
		colKeyShortlisted: {"true"},
	}
	conds, args := jobsFilter.Where(vals, 1)

	// Must produce a WHERE condition.
	if conds == "" {
		t.Fatalf("shortlisted=true filter: want non-empty conds, got empty")
	}
	// Condition must reference the column name, not inject the value.
	if !strings.Contains(conds, "shortlisted") {
		t.Errorf("shortlisted filter conds: want 'shortlisted' in %q", conds)
	}
	// Value 'true' must be a bind arg, not embedded in SQL.
	if strings.Contains(conds, "true") {
		t.Errorf("shortlisted filter: 'true' must be a bind arg, not in SQL conds %q", conds)
	}
	if len(args) == 0 {
		t.Fatalf("shortlisted=true filter: want bind args, got none")
	}

	// "false" not in Allowed list — verify it's not passed as a bind arg
	// (the filter framework drops unknown values).
	vals2 := url.Values{colKeyShortlisted: {"false"}}
	conds2, args2 := jobsFilter.Where(vals2, 1)
	// "false" IS in the Allowed list, so it should produce a condition.
	if conds2 == "" {
		t.Fatalf("shortlisted=false filter: want non-empty conds, got empty")
	}
	if len(args2) == 0 {
		t.Fatalf("shortlisted=false filter: want bind args, got none")
	}

	// Unknown value must be dropped.
	vals3 := url.Values{colKeyShortlisted: {"maybe"}}
	_, args3 := jobsFilter.Where(vals3, 1)
	if len(args3) != 0 {
		t.Errorf("shortlisted=maybe (unknown): want 0 bind args, got %d: %v", len(args3), args3)
	}
}
