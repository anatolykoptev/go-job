package search

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/anatolykoptev/go-engine/metrics"
	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-engine/websearch"
)

const (
	metricMojeekRequests = "mojeek_requests"
	mojeekEndpoint       = "https://www.mojeek.com/search"
	mojeekDirectScore    = 1.0
)

// SearchMojeekDirect queries Mojeek Search via HTML scraping.
// Returns nil, nil with a slog.Warn if 0 results were parsed (structure changed).
func SearchMojeekDirect(ctx context.Context, bc BrowserDoer, query string, m *metrics.Registry) ([]sources.Result, error) {
	if m != nil {
		m.Incr(metricMojeekRequests)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	u := mojeekEndpoint + "?q=" + url.QueryEscape(query)

	headers := websearch.ChromeHeaders()
	headers["accept"] = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"

	data, _, status, err := bc.Do(http.MethodGet, u, headers, nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusTooManyRequests {
		slog.Warn("mojeek: rate limited (429), skipping")
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("mojeek: unexpected status %d", status)
	}

	results, err := ParseMojeekHTML(data)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		slog.Warn("mojeek: 0 results parsed — HTML structure may have changed")
	}
	return results, nil
}

// ParseMojeekHTML extracts search results from Mojeek HTML response.
// Selectors verified against live Mojeek HTML on 2026-06-18:
//
//	ul.results-standard > li > h2 > a.title  (title + href)
//	li > p.s                                  (snippet)
//
// Exported for unit tests.
func ParseMojeekHTML(data []byte) ([]sources.Result, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("mojeek: html parse: %w", err)
	}

	var results []sources.Result
	doc.Find("ul.results-standard li").Each(func(_ int, s *goquery.Selection) {
		a := s.Find("h2 a.title")
		title := strings.TrimSpace(a.Text())
		href, _ := a.Attr("href")
		if title == "" || href == "" {
			return
		}
		snippet := strings.TrimSpace(s.Find("p.s").Text())
		results = append(results, sources.Result{
			Title:    title,
			URL:      href,
			Content:  snippet,
			Score:    mojeekDirectScore,
			Metadata: map[string]string{"engine": "mojeek"},
		})
	})
	return results, nil
}
