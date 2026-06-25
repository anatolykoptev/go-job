// Package shell provides the admin page chrome for go-panel: layout, sidebar
// navigation, static asset serving (htmx, pm7 CSS/JS), and security headers.
//
// The shell is templ-based: Layout renders the full HTML document wrapping a
// content component. For htmx fragment swaps, callers render the content
// component directly without the Layout wrapper.
//
// Static assets (htmx.min.js, pm7.css, pm7.js, admin.js) are embedded via
// embed.FS and served via StaticHandler at /admin/static/.
// No CDN dependency — safe for RU-audience deployments behind ТСПУ.
//
// Usage:
//
//	mux.Handle("/admin/static/", shell.StaticHandler())
//	// In a page handler:
//	if !render.IsHTMX(r) {
//	    return shell.Layout(title, nav, content).Render(r.Context(), w)
//	}
//	return content.Render(r.Context(), w)
package shell

import (
	"io/fs"
	"net/http"

	"github.com/anatolykoptev/go-panel/shell/internal/assets"
)

// StaticHandler returns an http.Handler that serves embedded static assets
// at /admin/static/<name> with 1-day Cache-Control.
// Mount at /admin/static/ in the admin mux.
func StaticHandler() http.Handler {
	sub, err := fs.Sub(assets.StaticFS, "static")
	if err != nil {
		panic("shell: failed to sub static FS: " + err.Error())
	}
	fsHandler := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		fsHandler.ServeHTTP(w, r)
	})
}

// SecurityHeaders sets the standard admin CSP + cache-control headers.
// Call this at the top of every authenticated handler.
// CSP allows htmx 2.x 'unsafe-eval' (used by hx-on::* handlers);
// trade-off accepted — admin renders no untrusted input.
func SecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; "+
			"img-src 'self' data:; "+
			"script-src 'self' 'unsafe-eval'; "+
			"style-src 'self' 'unsafe-inline'; "+
			"font-src 'self'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.Header().Set("Cache-Control", "no-store")
}
