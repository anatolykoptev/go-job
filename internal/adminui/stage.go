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

// stageOptions is the ordered option list for the inline stage dropdown.
// Each entry is (value, display label). The empty value "— stage —" is a
// placeholder sentinel for "no stage set yet" (maps to no rating row / stage="").
// Author-constant: no user text, no escaping required for values or labels.
// stageNoStage is the sentinel option value for "no stage set yet".
// Displayed as a placeholder; submitting this value is a no-op in the handler.
const stageNoStage = ""

var stageOptions = []struct{ Value, Label string }{
	{stageNoStage, "— stage —"},
	// Labels are the same string as hunt.Stage* constants (no separate label dict needed).
	{hunt.StageNew, hunt.StageNew},
	{hunt.StageInteresting, hunt.StageInteresting},
	{hunt.StageSaved, hunt.StageSaved},
	{hunt.StageDiscarded, hunt.StageDiscarded},
	{hunt.StageClaimed, hunt.StageClaimed},
	{hunt.StageApplied, hunt.StageApplied},
	{hunt.StageInterview, hunt.StageInterview},
	{hunt.StageOffer, hunt.StageOffer},
	{hunt.StageRejected, hunt.StageRejected},
}

// stageDropdownHTML returns XSS-safe HTML for an inline stage-change form cell.
// The form POSTs to /admin/jobs/{id}/stage. The <select> submits on change via
// onchange="this.form.submit()" so no submit button is needed.
//
// currentStage is the raw value from hunt_ratings.stage (or "" if no row). It
// is used only as a map key to select the `selected` attribute — never interpolated
// into HTML as text. csrfTok is the hex/decimal CSRF token from csrf.Issue.
//
// XSS safety: id is an int64 PK (author-constant), option values are the closed-enum
// stage constants (author-constant), csrfTok is the only caller-supplied string and
// is wrapped with html.EscapeString. No user-supplied text enters the HTML output.
func stageDropdownHTML(id int64, currentStage, csrfTok string) string {
	idStr := strconv.FormatInt(id, 10)
	var sb strings.Builder
	fmt.Fprintf(&sb,
		`<form method="POST" action="/admin/jobs/%s/stage" style="display:inline;margin:0">`+
			`<input type="hidden" name="%s" value="%s">`,
		idStr,
		html.EscapeString(csrf.FormField),
		html.EscapeString(csrfTok),
	)
	sb.WriteString(`<select name="stage" onchange="this.form.submit()" ` +
		`style="font-size:.8rem;padding:.1rem .2rem;border-radius:3px;border:1px solid var(--border,#ccc);background:var(--input-bg,#fff);color:var(--text,inherit);cursor:pointer" ` +
		`aria-label="Pipeline stage">`)
	for _, opt := range stageOptions {
		selected := ""
		if opt.Value == currentStage {
			selected = ` selected`
		}
		fmt.Fprintf(&sb, `<option value="%s"%s>%s</option>`, opt.Value, selected, opt.Label)
	}
	sb.WriteString(`</select></form>`)
	return sb.String()
}

// stageHandler returns an http.HandlerFunc that sets a job's pipeline stage via
// hunt_ratings. CSRF-protected (same pattern as rateHandler / shortlistHandler).
// Uses Store.SetStage so the existing note is preserved — the detail page's note
// field is never wiped by a table-row stage change.
// On success redirects to Referer (preserving filter state, same as shortlistHandler).
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
		// Empty string is the "no-op" sentinel from the placeholder option — skip the write.
		if stage == "" {
			http.Redirect(w, r, safeAdminReferer(r.Header.Get("Referer")), http.StatusSeeOther) //nolint:gosec // G710: safeAdminReferer validates.
			return
		}
		if !validHuntStages[stage] {
			http.Error(w, "invalid stage", http.StatusBadRequest)
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
