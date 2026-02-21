package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// SearchStartpageDirect queries Startpage directly using browser TLS fingerprint.
// Returns results compatible with the SearXNG pipeline.
func SearchStartpageDirect(ctx context.Context, bc *BrowserClient, query, language string) ([]SearxngResult, error) {
	if language == "" || language == "all" {
		language = "english"
	}

	metrics.DirectStartpageRequests.Add(1)

	formBody := fmt.Sprintf("query=%s&cat=web&language=%s", urlEncode(query), urlEncode(language))

	headers := ChromeHeaders()
	headers["referer"] = "https://www.startpage.com/"
	headers["content-type"] = "application/x-www-form-urlencoded"
	headers["accept"] = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"

	data, _, status, err := bc.Do("POST", "https://www.startpage.com/sp/search", headers, strings.NewReader(formBody))
	if err != nil {
		return nil, fmt.Errorf("startpage request: %w", err)
	}
	if status != 200 {
		return nil, fmt.Errorf("startpage status %d", status)
	}

	results, err := parseStartpageHTML(data)
	if err != nil {
		return nil, fmt.Errorf("startpage parse: %w", err)
	}

	slog.Debug("startpage direct results", slog.Int("count", len(results)), slog.String("query", query))
	return results, nil
}

// parseStartpageHTML extracts search results from Startpage HTML response.
func parseStartpageHTML(data []byte) ([]SearxngResult, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(data)))
	if err != nil {
		return nil, fmt.Errorf("goquery parse: %w", err)
	}

	var results []SearxngResult

	// Startpage result blocks: <div class="w-gl__result"> or <div class="result">
	doc.Find(".w-gl__result, .result").Each(func(i int, s *goquery.Selection) {
		// Title + URL from <a> inside heading
		link := s.Find("a.w-gl__result-title, h3 a, a.result-link").First()
		title := strings.TrimSpace(link.Text())
		href, exists := link.Attr("href")
		if !exists || title == "" {
			return
		}

		// Description
		desc := s.Find("p.w-gl__description, .w-gl__description, p.result-description").First()
		content := strings.TrimSpace(desc.Text())

		// Skip empty/ad results
		if href == "" || strings.Contains(href, "startpage.com/do/") {
			return
		}

		results = append(results, SearxngResult{
			Title:   title,
			Content: content,
			URL:     href,
			Score:   1.0,
		})
	})

	return results, nil
}

// urlEncode is a minimal URL encoding for form values.
func urlEncode(s string) string {
	return strings.NewReplacer(
		" ", "+",
		"&", "%26",
		"=", "%3D",
		"+", "%2B",
	).Replace(s)
}
