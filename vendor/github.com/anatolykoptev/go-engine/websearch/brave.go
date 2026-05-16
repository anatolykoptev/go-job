package websearch

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

const (
	braveEndpoint = "https://search.brave.com/search"
	braveReferer  = "https://search.brave.com/"
)

// Brave searches Brave Search via HTML scraping.
type Brave struct {
	browser BrowserDoer
}

// BraveOption configures Brave.
type BraveOption func(*Brave)

// WithBraveBrowser sets the BrowserDoer for HTTP requests.
func WithBraveBrowser(bc BrowserDoer) BraveOption {
	return func(b *Brave) { b.browser = bc }
}

// NewBrave creates a Brave Search scraper. A BrowserDoer must be provided via WithBraveBrowser.
func NewBrave(opts ...BraveOption) *Brave {
	b := &Brave{}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Search implements Provider. Queries Brave Search via GET.
func (b *Brave) Search(ctx context.Context, query string, opts SearchOpts) ([]Result, error) {
	if b.browser == nil {
		return nil, fmt.Errorf("brave: BrowserDoer is required (use WithBraveBrowser)")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	u := braveEndpoint + "?q=" + url.QueryEscape(query) + "&source=web"

	headers := ChromeHeaders()
	headers["referer"] = braveReferer
	headers["accept"] = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"

	data, _, status, err := b.browser.Do(http.MethodGet, u, headers, nil)
	if err != nil {
		return nil, fmt.Errorf("brave request: %w", err)
	}
	if isRateLimitStatus(status) {
		return nil, &ErrRateLimited{Engine: "brave"}
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("brave status %d", status)
	}
	if isBraveRateLimited(data) {
		return nil, &ErrRateLimited{Engine: "brave"}
	}

	results, err := ParseBraveHTML(data)
	if err != nil {
		return nil, fmt.Errorf("brave parse: %w", err)
	}

	slog.Debug("brave results", slog.Int("count", len(results)))
	return applyLimit(results, opts.Limit), nil
}

// ParseBraveHTML extracts search results from Brave Search HTML response.
func ParseBraveHTML(data []byte) ([]Result, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(data)))
	if err != nil {
		return nil, fmt.Errorf("goquery parse: %w", err)
	}

	var results []Result

	doc.Find("[data-pos]").Each(func(_ int, s *goquery.Selection) {
		title := strings.TrimSpace(s.Find(".title").First().Text())
		href, exists := s.Find("a[href^='http']").First().Attr("href")
		if !exists || title == "" || href == "" {
			return
		}

		desc := strings.TrimSpace(
			s.Find("[class*='content'][class*='t-primary']").First().Text(),
		)

		results = append(results, Result{
			Title:    title,
			Content:  desc,
			URL:      href,
			Score:    directResultScore,
			Metadata: map[string]string{"engine": "brave"},
		})
	})

	return results, nil
}

// isBraveRateLimited checks if Brave blocked the request.
func isBraveRateLimited(body []byte) bool {
	lower := bytes.ToLower(body)
	markers := [][]byte{
		[]byte("captcha"),
		[]byte("rate limit"),
		[]byte("too many requests"),
		[]byte("blocked"),
	}
	for _, m := range markers {
		if bytes.Contains(lower, m) {
			return true
		}
	}
	return false
}
