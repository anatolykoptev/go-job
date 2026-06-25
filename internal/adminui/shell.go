// Package adminui: shell.go provides the renderShell helper that wraps
// bespoke page content HTML in the go-panel shell.Layout chrome
// (sidebar nav, static assets, security headers).
package adminui

import (
	"net/http"

	"github.com/a-h/templ"
	"github.com/anatolykoptev/go-panel/resource"
	"github.com/anatolykoptev/go-panel/shell"
)

// renderShell writes a full-page response that wraps contentHTML inside the
// go-panel shell.Layout chrome. activeID matches a NavItem.ID in the sidebar
// so the active item is highlighted.
//
// Call after setting any response headers you need; renderShell writes the
// Content-Type header and calls shell.SecurityHeaders itself.
func renderShell(w http.ResponseWriter, r *http.Request, p *resource.Panel, title string, activeID string, contentHTML string) {
	shell.SecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	nav := p.NavItemsActive(activeID)
	content := templ.Raw(contentHTML)
	if err := shell.Layout(title, nav, content).Render(r.Context(), w); err != nil {
		// Headers already sent; log only.
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}
