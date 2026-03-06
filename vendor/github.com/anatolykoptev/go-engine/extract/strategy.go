package extract

import (
	"context"
	"net/url"
)

// Format specifies the output format for extracted content.
type Format string

const (
	// FormatText returns plain text (default).
	FormatText Format = "text"
	// FormatMarkdown returns markdown.
	FormatMarkdown Format = "markdown"
	// FormatHTML returns cleaned HTML.
	FormatHTML Format = "html"
)

// Strategy extracts structured content from raw HTML.
// The existing Extractor satisfies this interface.
type Strategy interface {
	Extract(ctx context.Context, body []byte, pageURL *url.URL) (*Result, error)
}
