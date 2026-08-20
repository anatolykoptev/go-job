package resource

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/a-h/templ"
	"github.com/anatolykoptev/go-panel/auth"
	"github.com/anatolykoptev/go-panel/shell"
)

// RenderPage renders content inside the panel shell (chrome + sidebar with
// activeID marked active), setting the HTML content-type and the standard admin
// security headers (CSP, X-Frame-Options, Cache-Control — same as resource page
// handlers). For consumers mounting bespoke routes alongside the resource
// framework. activeID matches a nav item's ID (use AddNav to register the entry);
// pass "" for no active highlight.
//
// The sidebar is context-filtered (navItemsFor): role-gated and Visible-hidden
// items are excluded for the current session.
func (p *Panel) RenderPage(w http.ResponseWriter, r *http.Request, title, activeID string, content templ.Component) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	shell.SecurityHeaders(w)
	ctx := shell.ContextWithChrome(r.Context(), p.chromeStateFrom(r))
	return shell.Layout(title, p.navItemsFor(r.Context(), activeID), content).Render(ctx, w)
}

// chromeStateFrom reads per-request chrome state from cookies and the session,
// returning a ChromeState ready to thread into Layout via ContextWithChrome.
//
// Cookie contracts:
//   - "sb-c"="1"  → Collapsed=true
//   - "sb-g"=<url-encoded comma-separated names> → CollapsedGroups map
//   - "sb-t"="light"|"dark" → Theme (absent/unrecognised = dark, backward-compat)
//
// Encoding: the whole sb-g value is URL-encoded (encodeURIComponent on the
// joined string); the server URL-unescapes before splitting on ','. Group names
// must not contain literal commas — see matching note in admin.js readGroups.
//
// Profile: populated from p.profileCfg (static defaults set by SetProfile) with
// Name and Role overlaid per-request from auth.SessionFrom when they are blank.
// For HMACAuth consumers, SessionFrom returns false → Profile stays zero →
// Layout renders the bare Logout footer (backward-compat).
//
// Lives in the resource layer (not shell) so shell stays net/http-free.
func (p *Panel) chromeStateFrom(r *http.Request) shell.ChromeState {
	var state shell.ChromeState
	if c, err := r.Cookie(shell.SidebarCookie); err == nil && c.Value == "1" {
		state.Collapsed = true
	}
	state.Theme = themeFromCookie(r)
	state.CollapsedGroups = collapsedGroupsFromCookie(r)
	state.Profile = p.profileFor(r)
	return state
}

// themeFromCookie reads sb-t and accepts ONLY a recognised value; anything else
// — absent, empty, garbage — returns "", which themeClass renders as dark.
//
// This is the load-bearing backward-compat invariant, and it is deliberately
// the ONLY place a cookie value becomes a Theme: three downstream admins pick
// this library up on a version bump without asking for a light theme, and none
// of them may change appearance until an operator clicks the toggle.
//
// Note this is not the last line of defence — themeClass is total, so an
// unrecognised value reaching ChromeState by any other route still renders
// dark. That redundancy is intentional: this gate stops junk being STORED,
// themeClass stops it being RENDERED, and they fail independently.
func themeFromCookie(r *http.Request) string {
	c, err := r.Cookie(shell.ThemeCookie)
	if err != nil {
		return ""
	}
	if c.Value == shell.ThemeLight || c.Value == shell.ThemeDark {
		return c.Value
	}
	return ""
}

// collapsedGroupsFromCookie reads sb-g: the whole value is URL-encoded
// (encodeURIComponent over the joined string), so it is unescaped before the
// split on ','. Group names must therefore not contain a literal comma — see
// the matching note in admin.js readGroups.
//
// A decode failure falls back to the verbatim value rather than dropping the
// state: a mangled cookie should cost the operator one wrong group, not every
// group they had collapsed. Returns nil when nothing is collapsed, which is the
// zero value Layout already handles.
func collapsedGroupsFromCookie(r *http.Request) map[string]bool {
	c, err := r.Cookie(shell.GroupsCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	raw, decErr := url.QueryUnescape(c.Value)
	if decErr != nil {
		raw = c.Value // use verbatim if decode fails
	}
	var out map[string]bool
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if out == nil {
			out = make(map[string]bool)
		}
		out[name] = true
	}
	return out
}

// profileFor starts from the static defaults SetProfile supplied and overlays
// the live session's Name/Role when they are blank. For HMACAuth consumers
// SessionFrom returns false, the profile stays zero, and Layout renders the
// bare Logout footer — the backward-compatible path.
func (p *Panel) profileFor(r *http.Request) shell.ProfileConfig {
	prof := p.profileCfg
	s, ok := auth.SessionFrom(r.Context())
	if !ok {
		return prof
	}
	if prof.Name == "" {
		prof.Name = s.UserID
	}
	if prof.Role == "" {
		prof.Role = s.Role
	}
	return prof
}

// RenderPageHTML is RenderPage for callers holding already-rendered,
// ALREADY-ESCAPED HTML (e.g. produced via html/template). The string is
// wrapped with templ.Raw and emitted verbatim - the caller is responsible for
// escaping.
//
// SECURITY: templ.Raw bypasses templ's context-aware auto-escaping. Never pass
// unescaped user-controlled or database-sourced strings to this function; doing
// so introduces XSS. Only pass strings produced by html/template execution or
// other provably safe HTML renderers.
func (p *Panel) RenderPageHTML(w http.ResponseWriter, r *http.Request, title, activeID, htmlContent string) error {
	return p.RenderPage(w, r, title, activeID, templ.Raw(htmlContent))
}

// RenderError renders content inside the panel shell (title+nav+content via
// shell.Layout) into an internal buffer, and writes it to w in ONE shot only
// once rendering has fully succeeded — so a render failure can never leak a
// partial body ahead of the error response. On failure it logs server-side
// (label identifies which page failed; err is never included in the client
// response, only in the log) and writes message as the client-facing 500
// body; on success it sets the HTML content-type and writes the buffered
// bytes.
//
// This is the extraction of go-grad's cabinet/overview.go renderLayoutPage
// (buffer-then-write, no partial write on a Render error), generalized for
// any go-panel consumer. nav is a caller-supplied parameter — exactly how
// go-grad computes it today, via its own closure — RenderError does not call
// navItemsFor or otherwise substitute its own nav computation, so a future
// PR can replace go-grad's renderLayoutPage with a direct call to
// RenderError, passing the same title/nav/content/label and its own
// (currently Russian) message string verbatim.
func (p *Panel) RenderError(ctx context.Context, w http.ResponseWriter, title string, nav []shell.NavItem, content templ.Component, label, message string) {
	var buf bytes.Buffer
	if err := shell.Layout(title, nav, content).Render(ctx, &buf); err != nil {
		slog.ErrorContext(ctx, "resource: render failed", "label", label, "err", err)
		http.Error(w, message, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}
