package adminui

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/anatolykoptev/go-panel/auth"
	"github.com/anatolykoptev/go-panel/csrf"
	"github.com/anatolykoptev/go_job/internal/hunt"
)

// sessionCookieCtxKey is the context key used to inject the raw session cookie
// value into the request context so jobsLister can generate per-request CSRF
// tokens for the shortlist-star inline form cells.
type sessionCookieCtxKey struct{}

// withSessionCookieContext wraps next, reading the named session cookie and
// storing its raw value under sessionCookieCtxKey in the request context.
// Value is "" when the cookie is absent (unauthenticated path — go-panel will
// redirect to login before the lister runs, so the token is never used in that case).
func withSessionCookieContext(cookieName string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		val := ""
		if c, err := r.Cookie(cookieName); err == nil {
			val = c.Value
		}
		ctx := context.WithValue(r.Context(), sessionCookieCtxKey{}, val)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// sessionCookieFrom extracts the raw session cookie value injected by
// withSessionCookieContext. Returns "" when not present.
func sessionCookieFrom(ctx context.Context) string {
	v, _ := ctx.Value(sessionCookieCtxKey{}).(string)
	return v
}

// starToggleHTML returns XSS-safe HTML for a shortlist star toggle form cell.
// The form POSTs to /admin/jobs/{id}/shortlist and redirects back to Referer
// (preserving the current filter state). csrfTok is the hex/decimal token from
// csrf.Issue — safe to embed directly as a form field value.
//
// The star glyph (★ / ☆) is author-constant and the id is an int64 primary key:
// neither contains user-supplied text, so no additional escaping is needed beyond
// the html.EscapeString on csrfTok (which protects against any unexpected char
// in the token format).
func starToggleHTML(id int64, shortlisted bool, csrfTok string) string {
	star := "☆"
	if shortlisted {
		star = "★"
	}
	idStr := strconv.FormatInt(id, 10)
	return fmt.Sprintf(
		`<form method="POST" action="/admin/jobs/%s/shortlist" style="display:inline;margin:0">`+
			`<input type="hidden" name="%s" value="%s">`+
			`<button type="submit" style="background:none;border:none;cursor:pointer;font-size:1rem;padding:0;line-height:1" title="Toggle shortlist">%s</button>`+
			`</form>`,
		idStr,
		html.EscapeString(csrf.FormField),
		html.EscapeString(csrfTok),
		star,
	)
}

// shortlistHandler returns an http.HandlerFunc that atomically flips
// hunt_jobs.shortlisted for the given {id}. CSRF-protected (same pattern as
// rateHandler). Redirects to Referer on success to preserve filter state, with
// /admin/jobs as the safe fallback.
func shortlistHandler(store *hunt.Store, a auth.Authenticator, csrfKey []byte) http.HandlerFunc {
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

		// CSRF verification: token must be bound to the current session cookie.
		tok := r.FormValue(csrf.FormField)
		sessVal := sessionValue(r, a.(cookieNamer).SessionCookieName())
		if err := csrf.Verify(csrfKey, sessVal, tok); err != nil {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}

		if _, err := store.ToggleShortlist(r.Context(), id64); err != nil {
			slog.Error("shortlistHandler: toggle", "id", id64, "err", err)
			http.Error(w, "update failed", http.StatusInternalServerError)
			return
		}

		// Redirect back to Referer to preserve the current filter state
		// (e.g., /admin/jobs?shortlisted=true returns to the shortlist view).
		// Validate: accept only same-origin paths that start with /admin/ to
		// prevent open redirect (gosec G710). Fallback to /admin/jobs.
		http.Redirect(w, r, safeAdminReferer(r.Header.Get("Referer")), http.StatusSeeOther) //nolint:gosec // G710: safeAdminReferer validates Referer: rejects absolute URLs (non-empty Host/Scheme), restricts path to /admin/* only.
	}
}

// safeAdminReferer validates a Referer header value and returns a safe redirect
// target. Accepts only paths (no scheme+host) that start with adminBasePath
// (/admin). Any absolute URL, path outside /admin, or empty value falls back to
// /admin/jobs. Prevents open-redirect (gosec G710).
func safeAdminReferer(ref string) string {
	const fallback = adminBasePath + "/jobs"
	if ref == "" {
		return fallback
	}
	u, err := url.Parse(ref)
	if err != nil {
		return fallback
	}
	// Reject any URL that carries a host (absolute URL / open redirect).
	if u.Host != "" || u.Scheme != "" {
		return fallback
	}
	path := u.Path
	if !strings.HasPrefix(path, adminBasePath+"/") && path != adminBasePath {
		return fallback
	}
	// Reconstruct as path+query only — drop fragment.
	out := path
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out
}
