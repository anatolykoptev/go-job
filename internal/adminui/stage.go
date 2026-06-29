package adminui

import (
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/anatolykoptev/go-panel/auth"
	"github.com/anatolykoptev/go-panel/csrf"
	"github.com/anatolykoptev/go_job/internal/hunt"
)

// stageDropdownHTML returns XSS-safe HTML for an inline pipeline-stage-change form cell.
// The form POSTs to /admin/jobs/{id}/stage. Stage options are pipeline-only after
// migration 012 (no triage values in the table inline dropdown).
//
// currentStage is the raw value from hunt_ratings.stage (or "" if no row). It
// is used only as an equality key to select the `selected` attribute — never
// interpolated into HTML as text. csrfTok is the hex/decimal CSRF token from
// csrf.Issue.
//
// XSS safety: id is an int64 PK (author-constant), option values are the
// closed-enum pipeline stage constants (author-constant), csrfTok is the only
// caller-supplied string and is wrapped with html.EscapeString. No
// user-supplied text enters the HTML output.
//
// Accessibility: no onchange auto-submit (WCAG 3.2.2 — On Input); the explicit
// "✓" submit button lets keyboard users confirm the selection without
// unintended navigation on arrow-key traversal.
//
// NOTE: mirrors status.go; extract a shared inlineEnumDropdown on the 3rd such field.
func stageDropdownHTML(id int64, currentStage, csrfTok string) string {
	idStr := strconv.FormatInt(id, 10)
	var sb strings.Builder
	fmt.Fprintf(&sb,
		`<form method="POST" action="/admin/jobs/%s/stage" style="display:inline;margin:0" onsubmit="this.querySelector('button[type=submit]').disabled=true">`+
			`<input type="hidden" name="%s" value="%s">`,
		idStr,
		html.EscapeString(csrf.FormField),
		html.EscapeString(csrfTok),
	)
	sb.WriteString(`<select name="stage" ` +
		`style="font-size:.8rem;padding:.1rem .2rem;border-radius:3px;border:1px solid var(--border,#1e2d4a);background:var(--bg-elevated,#1a2540);color:var(--text-primary,#e8edf5);cursor:pointer" ` +
		`aria-label="My pipeline">`)
	// Pipeline-only options — no triage values (those are managed via the detail-page triage form).
	sb.WriteString(pipelineOptgroupHTML(currentStage))
	sb.WriteString(`</select>` +
		`<button type="submit" aria-label="Save stage" ` +
		`style="background:none;border:none;cursor:pointer;font-size:.8rem;padding:.1rem .2rem;line-height:1;color:var(--text-secondary,#7b8ba8)">✓</button>` +
		`</form>`)
	return sb.String()
}

// stageHandler returns an http.HandlerFunc that sets a job's PIPELINE stage via
// hunt_ratings.stage. CSRF-protected. Uses Store.SetStage so the existing note is
// preserved. Pipeline-only: rejects any triage-axis value.
// On success redirects to Referer (preserving filter state).
func stageHandler(store *hunt.Store, adminUser string, a auth.Authenticator, csrfKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rawID := r.PathValue("id")
		id64, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil || id64 <= 0 {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}

		const maxBody = 4096
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}

		// CSRF verification bound to the session cookie.
		tok := r.FormValue(csrf.FormField)
		sessVal := sessionValue(r, a.(cookieNamer).SessionCookieName())
		if err := csrf.Verify(csrfKey, sessVal, tok); err != nil {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}

		stage := r.FormValue("stage")
		// stage=="" is the explicit "— clear —" option (SetStage("") blanks the column).
		// Only reject non-empty values that are not in the pipeline enum.
		if stage != "" && !validPipelineStages[stage] {
			http.Error(w, "invalid pipeline stage", http.StatusBadRequest)
			return
		}

		if err := store.SetStage(r.Context(), "job", id64, adminUser, stage); err != nil {
			slog.Error("stageHandler: set stage", "id", id64, "err", err)
			dest := safeAdminReferer(r.Header.Get("Referer"))
			if strings.Contains(dest, "?") {
				dest += "&err=stage-set-failed"
			} else {
				dest += "?err=stage-set-failed"
			}
			http.Redirect(w, r, dest, http.StatusSeeOther) //nolint:gosec // G710: safeAdminReferer validates.
			return
		}

		http.Redirect(w, r, safeAdminReferer(r.Header.Get("Referer")), http.StatusSeeOther) //nolint:gosec // G710: safeAdminReferer validates.
	}
}
