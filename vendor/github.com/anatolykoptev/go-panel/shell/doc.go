// Package shell provides the admin page chrome for go-panel.
//
// Layout (templ component) renders the full HTML document: html head, pm7 CSS,
// collapsible sidebar nav, and a content slot. For htmx fragment swaps, callers
// render the content component directly without the Layout wrapper.
//
// LoginPage (templ component) renders the standalone login form (no sidebar).
//
// StaticHandler serves embedded static assets (htmx.min.js, pm7.css, pm7.js,
// admin.js) at /admin/static/ with 1-day Cache-Control. No CDN — safe for
// RU-audience deployments behind ТСПУ.
//
// SecurityHeaders sets the admin CSP + cache-control headers on every response.
//
// Assets ported from go-nerv/internal/admin/templates/layout.html + static/.
package shell
