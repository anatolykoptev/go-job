package adminui

import (
	"net/url"
	"strings"
	"testing"
)

// TestStarToggleHTML_Unstarred verifies the ☆ state: hollow star, correct
// action URL, CSRF token present, aria-label=Add, aria-pressed=false, muted color.
// Red-on-revert: removing starToggleHTML or inverting the star glyph → fails.
func TestStarToggleHTML_Unstarred(t *testing.T) {
	got := starToggleHTML(42, false, "test-tok")
	for _, want := range []string{
		`action="/admin/jobs/42/shortlist"`,
		`method="POST"`,
		`☆`,
		`name="_csrf"`,
		`value="test-tok"`,
		`aria-label="Add to shortlist"`,
		`aria-pressed="false"`,
		`outline-offset:2px`,
		starColorMuted, // unstarred → muted grey
	} {
		if !strings.Contains(got, want) {
			t.Errorf("starToggleHTML(42, false): want %q in output, got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "★") {
		t.Errorf("starToggleHTML(42, false): should not contain filled star ★")
	}
	if strings.Contains(got, `aria-label="Remove`) {
		t.Errorf("starToggleHTML(42, false): aria-label must say Add, not Remove")
	}
	// Gold color must NOT appear in the unstarred state.
	if strings.Contains(got, starColorGold) {
		t.Errorf("starToggleHTML(42, false): gold color %q must not appear in unstarred state", starColorGold)
	}
	// Guard against a malformed tag (e.g. a stray `">>` closing the form),
	// which renders a literal `>` in every table row. `>>` must never appear.
	if strings.Contains(got, ">>") {
		t.Errorf("starToggleHTML: malformed markup, contains \">>\":\n%s", got)
	}
}

// TestStarToggleHTML_Starred verifies the ★ state: filled star, correct action URL,
// aria-label=Remove, aria-pressed=true, gold color.
// Red-on-revert: inverting the starred branch → ★ missing, aria-pressed="true" missing,
// or gold color absent.
func TestStarToggleHTML_Starred(t *testing.T) {
	got := starToggleHTML(99, true, "another-tok")
	for _, want := range []string{
		`★`,
		`action="/admin/jobs/99/shortlist"`,
		`aria-label="Remove from shortlist"`,
		`aria-pressed="true"`,
		starColorGold, // starred → gold
	} {
		if !strings.Contains(got, want) {
			t.Errorf("starToggleHTML(99, true): want %q in output, got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "☆") {
		t.Errorf("starToggleHTML(99, true): should not contain hollow star ☆")
	}
	if strings.Contains(got, `aria-pressed="false"`) {
		t.Errorf("starToggleHTML(99, true): aria-pressed must be true, not false")
	}
	// Muted color must NOT appear in the starred state (gold replaced it).
	if strings.Contains(got, starColorMuted) {
		t.Errorf("starToggleHTML(99, true): muted color %q must not appear in starred state", starColorMuted)
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

// TestJobsFilter_NoShortlistedFilter verifies the boolean shortlisted filter
// has been removed from jobsFilter. The star is now rating-backed (LEFT JOIN),
// not a separate column filter.
// Red-on-revert: re-adding the shortlisted filter → this assertion fails.
func TestJobsFilter_NoShortlistedFilter(t *testing.T) {
	// "shortlisted=true" must produce no WHERE condition — filter was removed.
	_, args := jobsFilter.Where(url.Values{"shortlisted": {"true"}}, 1)
	if len(args) != 0 {
		t.Errorf("shortlisted filter must be absent from jobsFilter: got %d bind args", len(args))
	}
}
