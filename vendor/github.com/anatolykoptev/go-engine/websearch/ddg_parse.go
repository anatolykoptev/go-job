package websearch

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

const ddgDJSTruncateLen = 200

// ddgResult represents a single DuckDuckGo search result from d.js.
type ddgResult struct {
	T string `json:"t"` // title
	A string `json:"a"` // abstract/content (HTML)
	U string `json:"u"` // URL
	C string `json:"c"` // content URL (alternative)
}

// ParseDDGHTML extracts search results from DDG HTML lite response.
func ParseDDGHTML(data []byte) ([]Result, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(data)))
	if err != nil {
		return nil, fmt.Errorf("goquery parse: %w", err)
	}

	var results []Result

	doc.Find(".result, .web-result").Each(func(_ int, s *goquery.Selection) {
		link := s.Find("a.result__a, .result__title a, a.result-link").First()
		title := strings.TrimSpace(link.Text())
		href, exists := link.Attr("href")
		if !exists || title == "" {
			return
		}

		href = DDGUnwrapURL(href)
		if href == "" {
			return
		}

		snippet := s.Find(".result__snippet, .result__body").First()
		content := strings.TrimSpace(snippet.Text())

		results = append(results, Result{
			Title:    title,
			Content:  content,
			URL:      href,
			Score:    directResultScore,
			Metadata: map[string]string{"engine": "ddg"},
		})
	})

	return results, nil
}

// DDGUnwrapURL extracts the actual URL from DDG redirect wrappers.
// DDG HTML wraps links as: //duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com&rut=...
func DDGUnwrapURL(href string) string {
	if strings.Contains(href, "duckduckgo.com/l/") || strings.Contains(href, "uddg=") {
		if u, err := url.Parse(href); err == nil {
			if uddg := u.Query().Get("uddg"); uddg != "" {
				return uddg
			}
		}
	}
	if strings.HasPrefix(href, "http") {
		return href
	}
	return ""
}

// ParseDDGResponse extracts search results from DDG d.js response.
// The response may be JSONP or raw JSON array.
func ParseDDGResponse(data []byte) ([]Result, error) {
	body := strings.TrimSpace(string(data))

	// Strip JSONP wrapper if present: DDGjsonp_xxx([...])
	if idx := strings.Index(body, "["); idx >= 0 {
		end := strings.LastIndex(body, "]")
		if end > idx {
			body = body[idx : end+1]
		}
	}

	var raw []ddgResult
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		snippet := body
		if len(snippet) > ddgDJSTruncateLen {
			snippet = snippet[:ddgDJSTruncateLen]
		}
		return nil, fmt.Errorf("ddg json parse: %w (first %d bytes: %s)", err, ddgDJSTruncateLen, snippet)
	}

	var results []Result
	for _, r := range raw {
		resultURL := r.U
		if resultURL == "" {
			resultURL = r.C
		}
		if resultURL == "" || r.T == "" {
			continue
		}
		if strings.HasPrefix(resultURL, "https://duckduckgo.com/") {
			continue
		}
		results = append(results, Result{
			Title:    CleanHTML(r.T),
			Content:  CleanHTML(r.A),
			URL:      resultURL,
			Score:    directResultScore,
			Metadata: map[string]string{"engine": "ddg"},
		})
	}

	return results, nil
}

// ExtractVQD extracts the VQD token from DDG response HTML.
func ExtractVQD(body string) string {
	for _, pat := range vqdPatterns {
		if m := pat.FindStringSubmatch(body); len(m) > 1 {
			return m[1]
		}
	}
	return ""
}
