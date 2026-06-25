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
//	    content, _ := render.Component(ctx, MyComponent())
//	    render.Page(w, r, p, "my-page", content)
//	}
package render

import (
	"bytes"
	"context"
	"html/template"
	"net/http"

	"github.com/a-h/templ"
)

// IsHTMX reports whether the request was made by htmx.
func IsHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// Component renders a templ component to an HTML string.
// Returns template.HTML for safe embedding in other templates or responses.
func Component(ctx context.Context, c templ.Component) (template.HTML, error) {
	var buf bytes.Buffer
	if err := c.Render(ctx, &buf); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil //nolint:gosec // content is templ-rendered, inherently escaped
}

// Fragment writes a templ component directly to the response as an HTML fragment.
// Use for htmx swap targets. Does not set Content-Type (caller's responsibility).
func Fragment(w http.ResponseWriter, r *http.Request, c templ.Component) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return c.Render(r.Context(), w)
}
