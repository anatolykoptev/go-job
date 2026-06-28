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

// mintStarCSRF issues a CSRF token bound to the session cookie in ctx.
// Shared helper used by jobsLister and shortlistLister to avoid duplicate
// sessVal/csrf.Issue two-liners.
func mintStarCSRF(ctx context.Context, csrfKey []byte) string {
	return csrf.Issue(csrfKey, sessionCookieFrom(ctx), csrf.DefaultTTL)
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
//
// Accessibility: aria-pressed reflects current starred state; aria-label
// describes the action resulting from the click (add vs. remove).
// outline-offset:2px ensures keyboard focus is visible when the UA's default
// button:focus-visible outline is absent (stripped by border:none reset).
func starToggleHTML(id int64, starred bool, csrfTok string) string {
	star := "☆"
	ariaLabel := "Add to shortlist"
	ariaPressed := "false"
	if starred {
		star = "★"
		ariaLabel = "Remove from shortlist"
		ariaPressed = "true"
	}
	idStr := strconv.FormatInt(id, 10)
	return fmt.Sprintf(
		`<form method="POST" action="/admin/jobs/%s/shortlist" style="display:inline;margin:0">`+
			`<input type="hidden" name="%s" value="%s">`+
			`<button type="submit"`+
			` style="background:none;border:none;cursor:pointer;font-size:1rem;padding:0;line-height:1;outline-offset:2px"`+
			` aria-label="%s" aria-pressed="%s"`+
			` title="%s">%s</button>`+
			`</form>`,
		idStr,
		html.EscapeString(csrf.FormField),
		html.EscapeString(csrfTok),
		ariaLabel,
		ariaPressed,
		ariaLabel,
		star,
	)
}

// shortlistHandler returns an http.HandlerFunc that toggles a job's shortlist
// membership via hunt_ratings (rating-backed star). CSRF-protected (same pattern
// as rateHandler). On success redirects to Referer to preserve filter state.
// On toggle error redirects to Referer with ?err=star-toggle-failed so the
// operator stays in the admin UI (no dead-end error page).
func shortlistHandler(store *hunt.Store, adminUser string, a auth.Authenticator, csrfKey []byte) http.HandlerFunc {
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

		if _, err := store.ToggleShortlistStar(r.Context(), id64, adminUser, shortlistActiveStages, hunt.StarSoftStages); err != nil {
			slog.Error("shortlistHandler: toggle star", "id", id64, "err", err)
			// Redirect back with an error param so the operator stays in the admin
			// UI rather than landing on a dead-end error page.
			dest := safeAdminReferer(r.Header.Get("Referer"))
			if strings.Contains(dest, "?") {
				dest += "&err=star-toggle-failed"
			} else {
				dest += "?err=star-toggle-failed"
			}
			http.Redirect(w, r, dest, http.StatusSeeOther) //nolint:gosec // G710: safeAdminReferer validates Referer: rejects absolute URLs (non-empty Host/Scheme), restricts path to /admin/* only.
			return
		}

		// Redirect back to Referer to preserve the current filter/sort state.
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
