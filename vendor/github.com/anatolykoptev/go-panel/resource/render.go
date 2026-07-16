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
	if c, err := r.Cookie(shell.GroupsCookie); err == nil && c.Value != "" {
		raw, decErr := url.QueryUnescape(c.Value)
		if decErr != nil {
			raw = c.Value // use verbatim if decode fails
		}
		for _, name := range strings.Split(raw, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if state.CollapsedGroups == nil {
				state.CollapsedGroups = make(map[string]bool)
			}
			state.CollapsedGroups[name] = true
		}
	}

	// Profile: start from static defaults; overlay live session fields.
	state.Profile = p.profileCfg
	if s, ok := auth.SessionFrom(r.Context()); ok {
		if state.Profile.Name == "" {
			state.Profile.Name = s.UserID
		}
		if state.Profile.Role == "" {
			state.Profile.Role = s.Role
		}
	}
	return state
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
