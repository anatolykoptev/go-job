package engine

import (
	"context"
	"net/url"
	"regexp"
	"strings"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/PuerkitoBio/goquery"
	readability "codeberg.org/readeck/go-readability/v2"
)

// FetchURLContent extracts main text content from a URL using go-readability.
// Falls back to goquery, then regex-based extraction on failure.
func FetchURLContent(ctx context.Context, rawURL string) (title, content string, err error) {
	metrics.FetchRequests.Add(1)
	defer func() {
		if err != nil {
			metrics.FetchErrors.Add(1)
		}
	}()

	ctx, cancel := context.WithTimeout(ctx, cfg.FetchTimeout)
	defer cancel()

	resp, err := fetchWithRetry(ctx, rawURL, true)
	if err != nil {
		return fetchWithFallback(ctx, rawURL)
	}
	defer resp.Body.Close()

	body, err := readResponseBody(resp)
	if err != nil {
		return fetchWithFallback(ctx, rawURL)
	}

	parsedURL, _ := url.Parse(rawURL)
	article, err := readability.FromReader(strings.NewReader(string(body)), parsedURL)
	if err != nil {
		return fetchWithGoquery(ctx, rawURL, body)
	}

	var htmlBuf strings.Builder
	_ = article.RenderHTML(&htmlBuf)

	md, err := htmltomarkdown.ConvertString(htmlBuf.String())
	if err != nil {
		var textBuf strings.Builder
		_ = article.RenderText(&textBuf)
		md = textBuf.String()
	}
	text := strings.TrimSpace(md)
	if len(text) > cfg.MaxContentChars {
		text = text[:cfg.MaxContentChars] + "..."
	}
	return article.Title(), text, nil
}

// fetchWithGoquery uses goquery for structured HTML parsing when readability fails.
func fetchWithGoquery(ctx context.Context, fetchURL string, body []byte) (title, content string, err error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return fetchWithFallback(ctx, fetchURL)
	}

	title = doc.Find("title").First().Text()
	if title == "" {
		doc.Find("meta[property=og:title]").Each(func(i int, s *goquery.Selection) {
			if title == "" {
				title, _ = s.Attr("content")
			}
		})
	}

	removeSelectors := []string{
		"script", "style", "noscript", "iframe", "svg",
		"header", "footer", "nav", "aside",
		".advertisement", ".ad", ".sidebar", ".comments",
		"[role=navigation]", "[role=banner]", "[role=contentinfo]",
	}
	doc.Find(strings.Join(removeSelectors, ", ")).Each(func(i int, s *goquery.Selection) {
		s.Remove()
	})

	contentSel := doc.Find("article, main, .content, .post-content, .article-content, #content").First()
	if contentSel.Length() == 0 {
		contentSel = doc.Find("body")
	}

	content = contentSel.Text()
	content = strings.TrimSpace(content)

	re := regexp.MustCompile(`\s+`)
	content = re.ReplaceAllString(content, " ")

	lines := strings.Split(content, "\n")
	var cleanLines []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			cleanLines = append(cleanLines, line)
		}
	}
	content = strings.Join(cleanLines, "\n")

	if len(content) > cfg.MaxContentChars {
		content = content[:cfg.MaxContentChars] + "..."
	}

	return title, content, nil
}

// fetchWithFallback uses regex-based HTML stripping when both readability and goquery fail.
func fetchWithFallback(ctx context.Context, rawURL string) (title, content string, err error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.FetchTimeout)
	defer cancel()

	resp, err := fetchWithRetry(ctx, rawURL, true)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	body, err := readResponseBody(resp)
	if err != nil {
		return "", "", err
	}

	html := string(body)

	titleRe := regexp.MustCompile(`(?i)<title[^>]*>([^<]+)</title>`)
	if m := titleRe.FindStringSubmatch(html); len(m) > 1 {
		title = strings.TrimSpace(m[1])
	}

	if title == "" {
		ogTitleRe := regexp.MustCompile(`(?i)<meta[^>]*property=["']og:title["'][^>]*content=["']([^"']+)["']`)
		if m := ogTitleRe.FindStringSubmatch(html); len(m) > 1 {
			title = strings.TrimSpace(m[1])
		}
	}

	re := regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	html = re.ReplaceAllString(html, "")
	re = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	html = re.ReplaceAllString(html, "")
	re = regexp.MustCompile(`(?is)<noscript[^>]*>.*?</noscript>`)
	html = re.ReplaceAllString(html, "")
	re = regexp.MustCompile(`(?is)<header[^>]*>.*?</header>`)
	html = re.ReplaceAllString(html, "")
	re = regexp.MustCompile(`(?is)<footer[^>]*>.*?</footer>`)
	html = re.ReplaceAllString(html, "")
	re = regexp.MustCompile(`(?is)<nav[^>]*>.*?</nav>`)
	html = re.ReplaceAllString(html, "")
	re = regexp.MustCompile(`(?is)<aside[^>]*>.*?</aside>`)
	html = re.ReplaceAllString(html, "")
	re = regexp.MustCompile(`(?is)<iframe[^>]*>.*?</iframe>`)
	html = re.ReplaceAllString(html, "")

	re = regexp.MustCompile(`<[^>]+>`)
	content = re.ReplaceAllString(html, "")

	content = strings.TrimSpace(content)
	re = regexp.MustCompile(`\s+`)
	content = re.ReplaceAllString(content, " ")

	lines := strings.Split(content, "\n")
	var cleanLines []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			cleanLines = append(cleanLines, line)
		}
	}
	content = strings.Join(cleanLines, "\n")

	if len(content) > cfg.MaxContentChars {
		content = content[:cfg.MaxContentChars] + "..."
	}

	return title, content, nil
}
