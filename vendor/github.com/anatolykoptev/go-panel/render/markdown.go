package render

// Markdown rendering via goldmark, ported from go-nerv/internal/admin/markdown.go.
// Ported verbatim except for package name change.

import (
	"bytes"
	"html/template"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// headingDemoter is a goldmark AST transformer that demotes all heading levels
// by exactly +1 (h1→h2, h2→h3, ..., h6→h6 clamped). This prevents content-
// authored <h1> from reversing WCAG 1.3.1 heading order relative to the page's
// <h2> title header in admin views.
type headingDemoter struct{}

func (headingDemoter) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if h, ok := n.(*ast.Heading); ok {
			const maxHeadingLevel = 6
			if h.Level < maxHeadingLevel {
				h.Level++
			}
			// h6 clamped: stays at 6.
		}
		return ast.WalkContinue, nil
	})
}

// md is a process-global goldmark instance with GFM extensions enabled
// (tables, strikethrough, task lists, autolinks), safe HTML rendering
// (no raw <script>), and heading-level demotion (+1 per level).
var md = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(
		parser.WithAutoHeadingID(),
		parser.WithASTTransformers(
			util.Prioritized(headingDemoter{}, 100),
		),
	),
	// WithUnsafe() is intentionally omitted — default is safe rendering.
)

// Markdown converts markdown source to safe HTML via goldmark.
// On parse error, returns an HTML-escaped fallback paragraph — never panics,
// never returns empty string for non-empty input.
func Markdown(src string) template.HTML {
	if src == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return template.HTML("<p class=\"md-error\">render failed: " + template.HTMLEscapeString(err.Error()) + "</p>") //nolint:gosec // operator-only admin surface
	}
	return template.HTML(buf.String()) //nolint:gosec // goldmark safe-by-default strips raw HTML
}
