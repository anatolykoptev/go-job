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
// Attempts to convert ContentNode to markdown; falls back to ContentText.
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
	var markdown string

	if result.ContentNode != nil {
		var htmlBuf bytes.Buffer
		if renderErr := html.Render(&htmlBuf, result.ContentNode); renderErr == nil {
			if md, mdErr := htmltomarkdown.ConvertString(htmlBuf.String()); mdErr == nil && strings.TrimSpace(md) != "" {
				markdown = md
				text = md
			}
		}
	}

	text = strings.TrimSpace(text)
	markdown = strings.TrimSpace(markdown)

	return &Result{
		Title:    result.Metadata.Title,
		Content:  e.truncate(text),
		Markdown: e.truncate(markdown),
	}, nil
}
