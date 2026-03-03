package extract

import (
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// reWhitespace collapses runs of whitespace into a single space.
var reWhitespace = regexp.MustCompile(`\s+`)

// removeSelectors are HTML elements stripped before text extraction.
var removeSelectors = strings.Join([]string{
	"script", "style", "noscript", "iframe", "svg",
	"header", "footer", "nav", "aside",
	".advertisement", ".ad", ".sidebar", ".comments",
	"[role=navigation]", "[role=banner]", "[role=contentinfo]",
}, ", ")

// contentSelectors are tried in order to find the main content element.
const contentSelectors = "article, main, .content, .post-content, .article-content, #content"

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

	content := contentSel.Text()
	content = strings.TrimSpace(content)
	content = reWhitespace.ReplaceAllString(content, " ")
	content = cleanLines(content)

	return &Result{
		Title:   title,
		Content: e.truncate(content),
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
