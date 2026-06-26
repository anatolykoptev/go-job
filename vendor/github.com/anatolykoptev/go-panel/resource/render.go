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
	return shell.Layout(title, p.NavItemsActive(activeID), content).Render(r.Context(), w)
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
