// Package assets embeds the shell static assets (htmx, pm7, admin.js).
package assets

import "embed"

//go:embed static/htmx.min.js static/pm7.css static/pm7.js static/admin.js
var StaticFS embed.FS
