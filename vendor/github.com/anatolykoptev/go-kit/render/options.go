package render

import "time"

// Option configures HTML rendering. Use With* constructors for readability;
// mutation via direct struct access is still supported for backward-compat.
type Option func(*Options)

// CoverPage configures the optional first-page cover. Use WithCoverPage.
type CoverPage struct {
	Title    string
	Subtitle string
	Author   string
	Date     string // empty -> today in YYYY-MM-DD
	Logo     string // path or data: URL
}

// ImageEmbedOptions configures inline-embedding of <img> resources into the
// rendered HTML as data: URLs, producing a self-contained PDF.
// Use WithImageEmbedding or WithImageEmbeddingOptions.
type ImageEmbedOptions struct {
	Enabled      bool
	Workspace    string        // root dir for resolving relative/file:// paths
	MaxBytes     int64         // per-image cap; 0 uses a sensible default (5 MB)
	AllowedHosts []string      // nil -> any public host (private IPs always rejected)
	Timeout      time.Duration // per-image fetch timeout; 0 uses 5s default
}

// Options holds HTML rendering configuration.
// Prefer the With* functional options for new code; the struct is exported for
// backward-compat and may gain fields over time.
type Options struct {
	// Title is the document title. When empty, the first H1 heading in the
	// markdown is used; if no H1 is found, the title falls back to "Document".
	Title string
	// CustomCSS is appended after the preset CSS and overrides it.
	CustomCSS string
	// Theme selects a registered theme CSS (default: "report").
	Theme string
	// TOC, when true, prepends a table of contents derived from document headings.
	TOC bool
	// CoverPage sets an optional first-page cover.
	CoverPage  *CoverPage
	ImageEmbed ImageEmbedOptions
	Mermaid    bool
	// Directives enables directive-block parsing (:::name{...} ... :::).
	// Default false for zero-risk behavior; callers opt in via WithDirectives.
	Directives bool
	// Width is the target image width in pixels; 0 means use the theme default.
	// Honored by the Typst image pipeline only.
	Width int
	// Height is the target image height in pixels; 0 means use the theme default.
	// Honored by the Typst image pipeline only.
	Height int
	// PPI is the pixels-per-inch density for PNG output. 0 falls back to 144.
	PPI int
}

// WithTitle sets the document title (otherwise derived from first H1).
func WithTitle(t string) Option { return func(o *Options) { o.Title = t } }

// WithCustomCSS appends additional CSS after the theme's default.
func WithCustomCSS(css string) Option { return func(o *Options) { o.CustomCSS = css } }

// WithTheme selects a registered theme by name. Unknown themes fall back to "report".
func WithTheme(name string) Option { return func(o *Options) { o.Theme = name } }

// WithTOC toggles auto-generated table of contents.
func WithTOC(enabled bool) Option { return func(o *Options) { o.TOC = enabled } }

// WithCoverPage sets a cover page to be rendered as the first page of the PDF,
// separated from the body by a page break.
func WithCoverPage(cp CoverPage) Option {
	return func(o *Options) { c := cp; o.CoverPage = &c }
}

// WithImageEmbedding toggles inline-embedding of <img> sources as data: URLs.
// Defaults apply (5 MB per image, 5 s timeout, public hosts only).
func WithImageEmbedding(enabled bool) Option {
	return func(o *Options) { o.ImageEmbed.Enabled = enabled }
}

// WithImageEmbeddingOptions configures inline-embedding in detail. Enabled is
// forced to true; use WithImageEmbedding(false) to disable.
func WithImageEmbeddingOptions(ie ImageEmbedOptions) Option {
	return func(o *Options) { ie.Enabled = true; o.ImageEmbed = ie }
}

// WithDirectives toggles directive-block parsing (:::name{...} ... :::).
// Default false. When enabled, the goldmark parser is extended with the
// directive block parser and every registered Handler participates in
// AST transform + HTML render.
func WithDirectives(enabled bool) Option {
	return func(o *Options) { o.Directives = enabled }
}

// WithMermaid toggles mermaid diagram rendering. When enabled, fenced code
// blocks with info "mermaid" render as SVG via a CDN-loaded mermaid.js.
func WithMermaid(enabled bool) Option {
	return func(o *Options) { o.Mermaid = enabled }
}
