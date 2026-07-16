// Package render provides htmx-aware response helpers: full-page layout vs
// bare fragment swap, and a goldmark markdown renderer.
//
// The core pattern (from oxpulse-admin / go-nerv):
//
//   - Full request (no HX-Request header) → render the layout shell wrapping the content.
//   - htmx request (HX-Request: true) → render only the content fragment for innerHTML swap.
//
// Usage:
//
//	func MyHandler(w http.ResponseWriter, r *http.Request) {
//	    if render.IsHTMX(r) {
//	        _ = render.Fragment(w, r, MyComponent())
//	        return
//	    }
//	    _ = shell.Layout(title, nav, MyComponent()).Render(r.Context(), w)
//	}
package render

import (
	"net/http"

	"github.com/a-h/templ"
)

// IsHTMX reports whether the request was made by htmx.
func IsHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// Fragment writes a templ component directly to the response as an HTML fragment.
// Use for htmx swap targets. Does not set Content-Type (caller's responsibility).
func Fragment(w http.ResponseWriter, r *http.Request, c templ.Component) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return c.Render(r.Context(), w)
}
