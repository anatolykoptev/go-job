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

// stageNoStage is the sentinel option value for "no stage set yet".
// Displayed as a disabled, hidden placeholder; submitting this value is a no-op
// in the handler. The option is kept selected when currentStage=="" so the
// browser shows "— stage —" rather than promoting the first real option.
const stageNoStage = ""

// stageDropdownHTML returns XSS-safe HTML for an inline stage-change form cell.
// The form POSTs to /admin/jobs/{id}/stage. Stage options are derived from
// hunt.AllStages (the single source of truth — no local duplicate list).
//
// currentStage is the raw value from hunt_ratings.stage (or "" if no row). It
// is used only as an equality key to select the `selected` attribute — never
// interpolated into HTML as text. csrfTok is the hex/decimal CSRF token from
// csrf.Issue.
//
// XSS safety: id is an int64 PK (author-constant), option values are the
// closed-enum stage constants (author-constant), csrfTok is the only
// caller-supplied string and is wrapped with html.EscapeString. No
// user-supplied text enters the HTML output.
//
// Accessibility: no onchange auto-submit (WCAG 3.2.2 — On Input); the explicit
// "✓" submit button lets keyboard users confirm the selection without
// unintended navigation on arrow-key traversal.
//
// TODO(stage): surface ?err= via a jobs-page banner (shared with star.go)
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
	// CSS uses tokens defined in go-panel/shell styles_templ.go:
	//   --bg-elevated:#1a2540  --text-primary:#e8edf5  --border:#1e2d4a
	// Fallbacks are the hardcoded token values so the select is readable on any
	// host that doesn't inject the shell stylesheet (e.g. tests / plain HTTP).
	sb.WriteString(`<select name="stage" ` +
		`style="font-size:.8rem;padding:.1rem .2rem;border-radius:3px;border:1px solid var(--border,#1e2d4a);background:var(--bg-elevated,#1a2540);color:var(--text-primary,#e8edf5);cursor:pointer" ` +
		`aria-label="Pipeline stage">`)
	// Placeholder: disabled + hidden so it is selectable by the browser when no
	// stage is set, but not choosable by the user (prevents accidental no-op
	// submissions after a real stage has been assigned).
	placeholderSelected := ""
	if currentStage == stageNoStage {
		placeholderSelected = ` selected`
	}
	fmt.Fprintf(&sb, `<option value="" disabled hidden%s>— stage —</option>`, placeholderSelected)
	// Real stage options derived from hunt.AllStages — single source of truth.
	// Labels equal the stage constant strings (no separate label dictionary needed).
	for _, s := range hunt.AllStages {
		selected := ""
		if s == currentStage {
			selected = ` selected`
		}
		// Option elements inherit the select's background/color on most browsers,
		// but explicit styling avoids white-on-white in WebKit when the user opens
		// the native picker (background:var(--bg-elevated) + color:var(--text-primary)).
		fmt.Fprintf(&sb,
			`<option value="%s"%s style="background:var(--bg-elevated,#1a2540);color:var(--text-primary,#e8edf5)">%s</option>`,
			s, selected, s)
	}
	// Submit button: explicit user gesture satisfies WCAG 3.2.2 (On Input).
	// Matches star.go button pattern: background:none;border:none;cursor:pointer.
	sb.WriteString(`</select>` +
		`<button type="submit" aria-label="Save stage" ` +
		`style="background:none;border:none;cursor:pointer;font-size:.8rem;padding:.1rem .2rem;line-height:1;color:var(--text-secondary,#7b8ba8)">✓</button>` +
		`</form>`)
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
