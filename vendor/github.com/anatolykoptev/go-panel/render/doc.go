// Package render provides htmx-aware response helpers and goldmark markdown rendering.
//
// IsHTMX detects htmx requests (HX-Request header).
// Fragment writes a templ.Component as an HTML fragment response.
// Markdown converts markdown to safe HTML via goldmark (GFM, heading demotion, no XSS).
//
// Ported from oxpulse-admin handler.go and go-nerv/internal/admin/markdown.go.
package render
