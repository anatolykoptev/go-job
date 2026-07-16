package adminui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/anatolykoptev/go_job/internal/hunt"
)

// TestStatusDropdownHTML_OpenSelected verifies the "open" option is selected when
// currentStatus="open" and the form targets the correct endpoint.
// Red-on-revert: removing selected-attribute logic → wrong option selected.
func TestStatusDropdownHTML_OpenSelected(t *testing.T) {
	got := statusDropdownHTML(42, hunt.StatusOpen, "test-tok")
	for _, want := range []string{
		`action="/admin/jobs/42/status"`,
		`method="POST"`,
		`name="status"`,
		`name="_csrf"`,
		`value="test-tok"`,
		`aria-label="Job posting status"`,
		`type="submit"`,
		`aria-label="Save status"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("statusDropdownHTML: want %q in output, got:\n%s", want, got)
		}
	}
	// "open" must be marked selected.
	if !strings.Contains(got, `value="open" selected`) {
		t.Errorf("statusDropdownHTML: want value=\"open\" selected, got:\n%s", got)
	}
	// onchange auto-submit must NOT be present (WCAG 3.2.2).
	if strings.Contains(got, `onchange=`) {
		t.Errorf("statusDropdownHTML: onchange auto-submit violates WCAG 3.2.2")
	}
}

// TestStatusDropdownHTML_ClosedSelected verifies the "closed" option is marked
// selected and "open" is not when currentStatus="closed".
// Red-on-revert: removing selected-attribute logic → wrong option marked.
func TestStatusDropdownHTML_ClosedSelected(t *testing.T) {
	got := statusDropdownHTML(7, hunt.StatusClosed, "tok")
	if !strings.Contains(got, `value="closed" selected`) {
		t.Errorf("statusDropdownHTML: want closed selected, got:\n%s", got)
	}
	if strings.Contains(got, `value="open" selected`) {
		t.Errorf("statusDropdownHTML: open must not be selected when status=closed")
	}
}

// TestStatusDropdownHTML_AllStatusesPresent verifies every hunt.AllStatuses entry
// appears as an option in the rendered dropdown.
// Red-on-revert: removing a status from hunt.AllStatuses → this assertion fails.
func TestStatusDropdownHTML_AllStatusesPresent(t *testing.T) {
	got := statusDropdownHTML(1, "", "tok")
	for _, s := range hunt.AllStatuses {
		if !strings.Contains(got, `value="`+s+`"`) {
			t.Errorf("statusDropdownHTML: want option value=%q in output", s)
		}
	}
}

// TestStatusDropdownHTML_ShellTokens verifies the select style uses the defined
// shell CSS tokens (--bg-elevated, --text-primary, --border) and NOT the
// undefined tokens (--input-bg, var(--text,)) that caused white-on-dark rendering.
// Red-on-revert: reverting to --input-bg → this test fails.
func TestStatusDropdownHTML_ShellTokens(t *testing.T) {
	got := statusDropdownHTML(1, "open", "tok")
	for _, token := range []string{"--bg-elevated", "--text-primary", "--border"} {
		if !strings.Contains(got, token) {
			t.Errorf("statusDropdownHTML: must reference shell token %q, not found:\n%s", token, got)
		}
	}
	for _, bad := range []string{"--input-bg", "var(--text,"} {
		if strings.Contains(got, bad) {
			t.Errorf("statusDropdownHTML: must not reference undefined token %q (white-on-dark regression):\n%s", bad, got)
		}
	}
}

// TestStatusDropdownHTML_OutOfEnumSentinel verifies that when currentStatus is not
// in hunt.AllStatuses a disabled/hidden placeholder is prepended, no real option is
// marked selected, and the raw value is visible (html-escaped) in the placeholder.
// Red-on-revert: removing the validHuntStatuses[currentStatus] branch → no sentinel,
// browser defaults to the first option, no-op ✓ save silently clobbers the value.
func TestStatusDropdownHTML_OutOfEnumSentinel(t *testing.T) {
	const legacy = "legacy_status"
	got := statusDropdownHTML(5, legacy, "tok")
	// Sentinel must be present: disabled, hidden, selected, showing the raw value.
	if !strings.Contains(got, `value="" disabled hidden selected`) {
		t.Errorf("out-of-enum: want disabled hidden selected placeholder, got:\n%s", got)
	}
	if !strings.Contains(got, "current: "+legacy) {
		t.Errorf("out-of-enum: want placeholder to contain %q, got:\n%s", "current: "+legacy, got)
	}
	// No real option should be marked selected.
	for _, s := range hunt.AllStatuses {
		if strings.Contains(got, `value="`+s+`" selected`) {
			t.Errorf("out-of-enum: real option %q must not be selected, got:\n%s", s, got)
		}
	}
}

// TestStatusDropdownHTML_DoubleSubmitGuard verifies the form has the onsubmit guard
// that disables the submit button to prevent double-POST.
// Red-on-revert: removing onsubmit → double-click sends two POSTs.
func TestStatusDropdownHTML_DoubleSubmitGuard(t *testing.T) {
	got := statusDropdownHTML(1, "open", "tok")
	if !strings.Contains(got, `onsubmit="this.querySelector('button[type=submit]').disabled=true"`) {
		t.Errorf("statusDropdownHTML: want onsubmit double-submit guard, got:\n%s", got)
	}
}

// TestStatusDropdownHTML_MalformedMarkup guards against stray ">>" in the output.
// Red-on-revert: introducing a bare `>` in the template → assertion fails.
func TestStatusDropdownHTML_MalformedMarkup(t *testing.T) {
	got := statusDropdownHTML(99, "archived", "csrf123")
	if strings.Contains(got, ">>") {
		t.Errorf("statusDropdownHTML: malformed markup, contains \">>\":\n%s", got)
	}
}

// TestStatusEnumSync verifies that validHuntStatuses stays in sync with hunt.AllStatuses.
// Red-on-revert: adding a status to hunt.AllStatuses without updating the map → fails.
func TestStatusEnumSync(t *testing.T) {
	t.Run("complete", func(t *testing.T) {
		for _, s := range hunt.AllStatuses {
			if !validHuntStatuses[s] {
				t.Errorf("validHuntStatuses: missing status %q (present in hunt.AllStatuses)", s)
			}
		}
	})
	t.Run("no_extras", func(t *testing.T) {
		canonical := make(map[string]bool, len(hunt.AllStatuses))
		for _, s := range hunt.AllStatuses {
			canonical[s] = true
		}
		for s := range validHuntStatuses {
			if !canonical[s] {
				t.Errorf("validHuntStatuses: extra status %q not in hunt.AllStatuses", s)
			}
		}
	})
	t.Run("dropdown_options", func(t *testing.T) {
		got := statusDropdownHTML(1, "open", "tok")
		for _, s := range hunt.AllStatuses {
			if !strings.Contains(got, `value="`+s+`"`) {
				t.Errorf("statusDropdownHTML: missing option for status %q", s)
			}
		}
	})
}

// TestStatusHandler_BadID verifies a non-numeric id returns 400.
func TestStatusHandler_BadID(t *testing.T) {
	handler := statusHandler(nil)

	form := url.Values{"status": {"open"}}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/admin/jobs/abc/status",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "abc")

	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("statusHandler: want 400 for bad id, got %d", rr.Code)
	}
}

// TestStatusHandler_RejectsInvalidStatus verifies that an unknown status value
// returns 400 without writing to the store (nil store → panic if write attempted).
// Red-on-revert: removing validHuntStatuses check → invalid value reaches store call.
func TestStatusHandler_RejectsInvalidStatus(t *testing.T) {
	// nil store — any write attempt panics, proving the guard fires first.
	handler := statusHandler(nil)

	form := url.Values{
		"status": {"hacked'; DROP TABLE hunt_jobs;--"},
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/admin/jobs/1/status",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "1")

	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("statusHandler: want 400 for invalid status, got %d: %s", rr.Code, rr.Body.String())
	}
}
