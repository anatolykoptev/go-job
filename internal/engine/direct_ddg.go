package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

var vqdPatterns = []*regexp.Regexp{
	regexp.MustCompile(`vqd='([^']+)'`),
	regexp.MustCompile(`vqd="([^"]+)"`),
	regexp.MustCompile(`vqd=([a-zA-Z0-9_-]+)`),
}

// ddgResult represents a single DuckDuckGo search result from d.js.
type ddgResult struct {
	T string `json:"t"` // title
	A string `json:"a"` // abstract/content (HTML)
	U string `json:"u"` // URL
	C string `json:"c"` // content URL (alternative)
}

// SearchDDGDirect queries DuckDuckGo directly using browser TLS fingerprint.
// Uses the HTML lite endpoint (html.duckduckgo.com/html) as primary,
// falls back to d.js JSON API if HTML parsing fails.
func SearchDDGDirect(ctx context.Context, bc *BrowserClient, query, region string) ([]SearxngResult, error) {
	if region == "" {
		region = "wt-wt"
	}

	metrics.DirectDDGRequests.Add(1)

	// Primary: HTML lite endpoint (more reliable, no VQD needed)
	results, err := ddgSearchHTML(ctx, bc, query, region)
	if err == nil && len(results) > 0 {
		slog.Debug("ddg direct results (html)", slog.Int("count", len(results)))
		return results, nil
	}
	if err != nil {
		slog.Debug("ddg html failed, trying d.js", slog.Any("error", err))
	}

	// Fallback: d.js JSON API (needs VQD token)
	vqd, err := ddgGetVQD(ctx, bc, query)
	if err != nil {
		return nil, fmt.Errorf("ddg vqd: %w", err)
	}
	results, err = ddgSearchDJS(ctx, bc, query, vqd, region)
	if err != nil {
		return nil, fmt.Errorf("ddg d.js: %w", err)
	}

	slog.Debug("ddg direct results (d.js)", slog.Int("count", len(results)))
	return results, nil
}

// ddgSearchHTML queries DDG via the HTML lite endpoint and parses results.
func ddgSearchHTML(ctx context.Context, bc *BrowserClient, query, region string) ([]SearxngResult, error) {
	formBody := fmt.Sprintf("q=%s&kl=%s&df=", url.QueryEscape(query), url.QueryEscape(region))

	headers := ChromeHeaders()
	headers["referer"] = "https://html.duckduckgo.com/"
	headers["content-type"] = "application/x-www-form-urlencoded"

	data, _, status, err := bc.Do("POST", "https://html.duckduckgo.com/html/", headers, strings.NewReader(formBody))
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("ddg html status %d", status)
	}

	return parseDDGHTML(data)
}

// parseDDGHTML extracts search results from DDG HTML lite response.
func parseDDGHTML(data []byte) ([]SearxngResult, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(data)))
	if err != nil {
		return nil, fmt.Errorf("goquery parse: %w", err)
	}

	var results []SearxngResult

	doc.Find(".result, .web-result").Each(func(i int, s *goquery.Selection) {
		// Title + URL
		link := s.Find("a.result__a, .result__title a, a.result-link").First()
		title := strings.TrimSpace(link.Text())
		href, exists := link.Attr("href")
		if !exists || title == "" {
			return
		}

		// DDG wraps URLs in redirects — extract actual URL
		href = ddgUnwrapURL(href)
		if href == "" {
			return
		}

		// Snippet
		snippet := s.Find(".result__snippet, .result__body").First()
		content := strings.TrimSpace(snippet.Text())

		results = append(results, SearxngResult{
			Title:   title,
			Content: content,
			URL:     href,
			Score:   1.0,
		})
	})

	return results, nil
}

// ddgUnwrapURL extracts the actual URL from DDG redirect wrappers.
// DDG HTML wraps links as: //duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com&rut=...
func ddgUnwrapURL(href string) string {
	if strings.Contains(href, "duckduckgo.com/l/") || strings.Contains(href, "uddg=") {
		if u, err := url.Parse(href); err == nil {
			if uddg := u.Query().Get("uddg"); uddg != "" {
				return uddg
			}
		}
	}
	// Already a direct URL
	if strings.HasPrefix(href, "http") {
		return href
	}
	return ""
}

// ddgGetVQD fetches the VQD token required for DuckDuckGo searches.
func ddgGetVQD(ctx context.Context, bc *BrowserClient, query string) (string, error) {
	u := "https://duckduckgo.com/?q=" + url.QueryEscape(query)

	headers := ChromeHeaders()
	headers["referer"] = "https://duckduckgo.com/"

	data, _, status, err := bc.Do("GET", u, headers, nil)
	if err != nil {
		return "", err
	}
	if status != 200 {
		return "", fmt.Errorf("ddg homepage status %d", status)
	}

	body := string(data)
	for _, pat := range vqdPatterns {
		if m := pat.FindStringSubmatch(body); len(m) > 1 {
			return m[1], nil
		}
	}

	return "", fmt.Errorf("vqd token not found in response (%d bytes)", len(data))
}

// ddgSearchDJS queries DDG via the d.js JSON API (fallback).
func ddgSearchDJS(ctx context.Context, bc *BrowserClient, query, vqd, region string) ([]SearxngResult, error) {
	params := url.Values{
		"q":   {query},
		"vqd": {vqd},
		"kl":  {region},
		"df":  {""},
		"l":   {"us-en"},
		"o":   {"json"},
	}
	u := "https://links.duckduckgo.com/d.js?" + params.Encode()

	headers := ChromeHeaders()
	headers["referer"] = "https://duckduckgo.com/"
	headers["accept"] = "application/json, text/javascript, */*; q=0.01"

	data, _, status, err := bc.Do("GET", u, headers, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 && status != 202 {
		return nil, fmt.Errorf("ddg d.js status %d", status)
	}

	return parseDDGResponse(data)
}

// parseDDGResponse extracts search results from DDG d.js response.
// The response may be JSONP or raw JSON array.
func parseDDGResponse(data []byte) ([]SearxngResult, error) {
	body := strings.TrimSpace(string(data))

	// Strip JSONP wrapper if present: DDGjsonp_xxx({results:[...]})
	if idx := strings.Index(body, "["); idx >= 0 {
		end := strings.LastIndex(body, "]")
		if end > idx {
			body = body[idx : end+1]
		}
	}

	var raw []ddgResult
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		return nil, fmt.Errorf("ddg json parse: %w (first 200 bytes: %s)", err, Truncate(body, 200))
	}

	var results []SearxngResult
	for _, r := range raw {
		resultURL := r.U
		if resultURL == "" {
			resultURL = r.C
		}
		if resultURL == "" || r.T == "" {
			continue
		}
		// Skip DDG internal/ad entries
		if strings.HasPrefix(resultURL, "https://duckduckgo.com/") {
			continue
		}
		results = append(results, SearxngResult{
			Title:   CleanHTML(r.T),
			Content: CleanHTML(r.A),
			URL:     resultURL,
			Score:   1.0, // direct scraper results get full score
		})
	}

	return results, nil
}

// extractVQD extracts the VQD token from DDG response HTML (exported for tests).
func extractVQD(body string) string {
	for _, pat := range vqdPatterns {
		if m := pat.FindStringSubmatch(body); len(m) > 1 {
			return m[1]
		}
	}
	return ""
}
