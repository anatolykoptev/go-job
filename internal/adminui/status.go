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

// validHuntStatuses is the allowlist of accepted status values for hunt_jobs.status.
// Derived from hunt.AllStatuses — the single source of truth for the status enum.
var validHuntStatuses = func() map[string]bool {
	m := make(map[string]bool, len(hunt.AllStatuses))
	for _, s := range hunt.AllStatuses {
		m[s] = true
	}
	return m
}()

// statusDropdownHTML returns XSS-safe HTML for an inline status-change form on the
// job detail page. The form POSTs to /admin/jobs/{id}/status. Status options are
// derived from hunt.AllStatuses (the single source of truth — no local duplicate list).
//
// currentStatus is the raw value from hunt_jobs.status. It is used only as an
// equality key to select the `selected` attribute — never interpolated into HTML as
// text. csrfTok is the hex/decimal CSRF token from csrf.Issue.
//
// XSS safety: id is an int64 PK (author-constant), option values are the closed-enum
// status constants (author-constant), csrfTok is the only caller-supplied string and
// is wrapped with html.EscapeString. No user-supplied text enters the HTML output.
//
// Accessibility: no onchange auto-submit (WCAG 3.2.2 — On Input); the explicit "✓"
// submit button lets keyboard users confirm the selection without unintended
// navigation on arrow-key traversal.
//
// CSS tokens are from go-panel/shell styles_templ.go:
//
//	--bg-elevated:#1a2540  --text-primary:#e8edf5  --border:#1e2d4a
//
// Fallbacks are the hardcoded token values so the select is readable on any host
// that doesn't inject the shell stylesheet.
func statusDropdownHTML(id int64, currentStatus, csrfTok string) string {
	idStr := strconv.FormatInt(id, 10)
	var sb strings.Builder
	fmt.Fprintf(&sb,
		`<form method="POST" action="/admin/jobs/%s/status" style="display:inline;margin:0">`+
			`<input type="hidden" name="%s" value="%s">`,
		idStr,
		html.EscapeString(csrf.FormField),
		html.EscapeString(csrfTok),
	)
	sb.WriteString(`<select name="status" ` +
		`style="font-size:.8rem;padding:.1rem .2rem;border-radius:3px;border:1px solid var(--border,#1e2d4a);background:var(--bg-elevated,#1a2540);color:var(--text-primary,#e8edf5);cursor:pointer" ` +
		`aria-label="Job posting status">`)
	for _, s := range hunt.AllStatuses {
		selected := ""
		if s == currentStatus {
			selected = ` selected`
		}
		fmt.Fprintf(&sb,
			`<option value="%s"%s style="background:var(--bg-elevated,#1a2540);color:var(--text-primary,#e8edf5)">%s</option>`,
			s, selected, s)
	}
	sb.WriteString(`</select>` +
		`<button type="submit" aria-label="Save status" ` +
		`style="background:none;border:none;cursor:pointer;font-size:.8rem;padding:.1rem .2rem;line-height:1;color:var(--text-secondary,#7b8ba8)">✓</button>` +
		`</form>`)
	return sb.String()
}

// statusHandler returns an http.HandlerFunc that updates hunt_jobs.status.
// CSRF-protected (same pattern as stageHandler / rateHandler / shortlistHandler).
// Invalid status values → 400, no write. Unknown id → 404.
// On success redirects to Referer (preserving filter state).
func statusHandler(store *hunt.Store, a auth.Authenticator, csrfKey []byte) http.HandlerFunc {
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

		status := r.FormValue("status")
		if !validHuntStatuses[status] {
			http.Error(w, "invalid status", http.StatusBadRequest)
			return
		}

		if err := store.SetStatus(r.Context(), id64, status); err != nil {
			slog.Error("statusHandler: set status", "id", id64, "err", err)
			dest := safeAdminReferer(r.Header.Get("Referer"))
			if strings.Contains(dest, "?") {
				dest += "&err=status-set-failed"
			} else {
				dest += "?err=status-set-failed"
			}
			http.Redirect(w, r, dest, http.StatusSeeOther) //nolint:gosec // G710: safeAdminReferer validates.
			return
		}

		http.Redirect(w, r, safeAdminReferer(r.Header.Get("Referer")), http.StatusSeeOther) //nolint:gosec // G710: safeAdminReferer validates.
	}
}
