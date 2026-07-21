package resource

import (
	"fmt"
	"html"
	"net/url"
	"strconv"
)

// CrossLinkCell is the framework's cross-link renderer, promoted from
// go-grad/internal/admin/crosslink.go. It renders an XSS-safe HTML anchor that
// links from one resource's cell to another resource's detail/list route on the
// same panel. Consumers pass the panel's basePath (e.g. "/admin") rather than a
// hardcoded "/admin" so the link stays correct under custom mounts (ADR-4,
// ADR-8).
//
// Security: the href is built from URL-path-escaped segments while the anchor
// text is HTML-escaped for text context. This fixes the go-grad bug where
// html.EscapeString was applied to the id inside the href (text-context
// escaping applied to a URL path), which both corrupted legitimate ids
// containing '&' and left path-traversal payloads un-neutralized.
//
// label is HTML-escaped for text context; id and resourceName are URL-escaped
// for path context. Do NOT use the label in JavaScript/URL contexts without
// additional escaping.
func CrossLinkCell(basePath, resourceName, id, label string) string {
	return fmt.Sprintf(`<a href="%s/%s/%s">%s</a>`,
		html.EscapeString(basePath),
		url.PathEscape(resourceName),
		url.PathEscape(id),
		html.EscapeString(label),
	)
}

// CrossLinkCellInt is a convenience wrapper that converts an int64 id to its
// string form and delegates to CrossLinkCell. Useful for resources whose
// primary key is numeric.
func CrossLinkCellInt(basePath, resourceName string, id int64, label string) string {
	return CrossLinkCell(basePath, resourceName, strconv.FormatInt(id, 10), label)
}
