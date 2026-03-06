package extract

import (
	"bytes"
	"net/url"
	"strings"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	trafilatura "github.com/markusmobius/go-trafilatura"
	"golang.org/x/net/html"
)

// extractTrafilatura uses go-trafilatura with EnableFallback and FavorRecall.
// Output format is determined by e.format.
func (e *Extractor) extractTrafilatura(body []byte, pageURL *url.URL) (*Result, error) {
	result, err := trafilatura.Extract(bytes.NewReader(body), trafilatura.Options{
		OriginalURL:     pageURL,
		EnableFallback:  true,
		Focus:           trafilatura.FavorRecall,
		ExcludeComments: true,
	})
	if err != nil {
		return nil, err
	}

	text := result.ContentText
	content := e.formatTrafilatura(result, text)

	return &Result{
		Title:   result.Metadata.Title,
		Content: e.truncate(content),
		Format:  e.format,
	}, nil
}

// formatTrafilatura converts trafilatura result to the requested format.
func (e *Extractor) formatTrafilatura(result *trafilatura.ExtractResult, text string) string {
	switch e.format {
	case FormatHTML:
		if s := renderContentNode(result); s != "" {
			return s
		}
		return strings.TrimSpace(text)
	case FormatMarkdown:
		if s := renderContentNodeAsMarkdown(result); s != "" {
			return s
		}
		return strings.TrimSpace(text)
	default: // FormatText
		if s := renderContentNodeAsMarkdown(result); s != "" {
			return s
		}
		return strings.TrimSpace(text)
	}
}

// renderContentNode renders ContentNode to HTML string.
func renderContentNode(result *trafilatura.ExtractResult) string {
	if result.ContentNode == nil {
		return ""
	}
	var buf bytes.Buffer
	if err := html.Render(&buf, result.ContentNode); err != nil {
		return ""
	}
	return strings.TrimSpace(buf.String())
}

// renderContentNodeAsMarkdown renders ContentNode to markdown.
func renderContentNodeAsMarkdown(result *trafilatura.ExtractResult) string {
	raw := renderContentNode(result)
	if raw == "" {
		return ""
	}
	md, err := htmltomarkdown.ConvertString(raw)
	if err != nil || strings.TrimSpace(md) == "" {
		return ""
	}
	return strings.TrimSpace(md)
}
