package extract

import (
	"regexp"
	"strings"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/PuerkitoBio/goquery"
)

// reWhitespace collapses runs of whitespace into a single space.
var reWhitespace = regexp.MustCompile(`\s+`)

// removeSelectors are HTML elements stripped before text extraction.
var removeSelectors = strings.Join([]string{
	// Standard boilerplate.
	"script", "style", "noscript", "iframe", "svg",
	"header", "footer", "nav", "aside",
	// Ads and non-content.
	".advertisement", ".ad", ".sidebar", ".comments",
	".cookie-banner", ".popup", ".modal", ".newsletter-signup",
	".social-share", ".share-buttons",
	// ARIA and HTML5 hidden.
	"[role=navigation]", "[role=banner]", "[role=contentinfo]",
	"[aria-hidden=true]", "[hidden]",
}, ", ")

// contentSelectors are tried in order to find the main content element.
const contentSelectors = "article, main, .content, .post-content, .article-content, #content"

// allowedAttrs is the whitelist for attribute stripping.
var allowedAttrs = map[string]bool{
	"href": true, "src": true, "alt": true, "title": true,
	"lang": true, "colspan": true, "rowspan": true,
}

// stripAttributes removes all attributes except allowedAttrs from all elements.
func stripAttributes(doc *goquery.Document) {
	doc.Find("*").Each(func(_ int, s *goquery.Selection) {
		node := s.Get(0)
		kept := node.Attr[:0]
		for _, a := range node.Attr {
			if allowedAttrs[a.Key] {
				kept = append(kept, a)
			}
		}
		node.Attr = kept
	})
}

// extractGoquery uses goquery CSS selectors to extract main content.
func (e *Extractor) extractGoquery(body []byte) (*Result, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}

	// Extract title.
	title := doc.Find("title").First().Text()
	if title == "" {
		doc.Find(`meta[property="og:title"]`).Each(func(_ int, s *goquery.Selection) {
			if title == "" {
				title, _ = s.Attr("content")
			}
		})
	}

	// Remove boilerplate elements.
	doc.Find(removeSelectors).Each(func(_ int, s *goquery.Selection) {
		s.Remove()
	})

	// Find main content container.
	contentSel := doc.Find(contentSelectors).First()
	if contentSel.Length() == 0 {
		contentSel = doc.Find("body")
	}

	var content string
	switch e.format {
	case FormatHTML:
		stripAttributes(doc)
		content, _ = contentSel.Html()
		content = strings.TrimSpace(content)
	case FormatMarkdown:
		rawHTML, _ := contentSel.Html()
		if md, mdErr := htmltomarkdown.ConvertString(rawHTML); mdErr == nil {
			content = strings.TrimSpace(md)
		}
	default: // FormatText
		content = contentSel.Text()
		content = strings.TrimSpace(content)
		content = reWhitespace.ReplaceAllString(content, " ")
		content = cleanLines(content)
	}

	return &Result{
		Title:   title,
		Content: e.truncate(content),
		Format:  e.format,
	}, nil
}

// cleanLines removes empty lines and trims each line.
func cleanLines(s string) string {
	lines := strings.Split(s, "\n")
	clean := lines[:0]
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			clean = append(clean, l)
		}
	}
	return strings.Join(clean, "\n")
}
