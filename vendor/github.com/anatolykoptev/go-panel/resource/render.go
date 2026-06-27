package resource

import (
	"net/http"

	"github.com/a-h/templ"
	"github.com/anatolykoptev/go-panel/shell"
)

// RenderPage renders content inside the panel shell (chrome + sidebar with
// activeID marked active), setting the HTML content-type and the standard admin
// security headers (CSP, X-Frame-Options, Cache-Control — same as resource page
// handlers). For consumers mounting bespoke routes alongside the resource
// framework. activeID matches a nav item's ID (use AddNav to register the entry);
// pass "" for no active highlight.
func (p *Panel) RenderPage(w http.ResponseWriter, r *http.Request, title, activeID string, content templ.Component) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	shell.SecurityHeaders(w)
	ctx := shell.ContextWithSidebar(r.Context(), sidebarStateFrom(r))
	return shell.Layout(title, p.NavItemsActive(activeID), content).Render(ctx, w)
}

// sidebarStateFrom reads the collapse cookie from the request.
// Cookie "sb-c"="1" → Collapsed=true; absent or any other value → Collapsed=false.
// Lives in the resource layer (not shell) so shell stays net/http-free.
func sidebarStateFrom(r *http.Request) shell.SidebarState {
	if c, err := r.Cookie(shell.SidebarCookie); err == nil && c.Value == "1" {
		return shell.SidebarState{Collapsed: true}
	}
	return shell.SidebarState{}
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
