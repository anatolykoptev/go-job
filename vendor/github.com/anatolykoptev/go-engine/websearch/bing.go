//nolint:goconst,dupl
package websearch

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

const (
	bingEndpoint = "https://www.bing.com/search"
	bingReferer  = "https://www.bing.com/"
)

// Bing searches Bing via HTML scraping.
type Bing struct {
	browser BrowserDoer
}

// BingOption configures Bing.
type BingOption func(*Bing)

// WithBingBrowser sets the BrowserDoer for HTTP requests.
func WithBingBrowser(bc BrowserDoer) BingOption {
	return func(b *Bing) { b.browser = bc }
}

// BingSearchURL returns the GET URL for the Bing Search HTML SERP endpoint.
// P2 feeds this URL to ox-browser /fetch so the SERP request shape stays
// single-owned in websearch (ADR-8: const host + url.QueryEscape only).
func BingSearchURL(query string, opts ...SearchOpts) string {
	u := bingEndpoint + "?q=" + url.QueryEscape(query)
	if len(opts) > 0 {
		if freshness := timeRangeToBing(opts[0].TimeRange); freshness != "" {
			u += "&freshness=" + url.QueryEscape(freshness)
		}
	}
	return u
}

// NewBing creates a Bing Search scraper. A BrowserDoer must be provided via WithBingBrowser.
func NewBing(opts ...BingOption) *Bing {
	b := &Bing{}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Search implements Provider. Queries Bing Search via GET.
func (b *Bing) Search(ctx context.Context, query string, opts SearchOpts) ([]Result, error) {
	if b.browser == nil {
		return nil, errors.New("bing: BrowserDoer is required (use WithBingBrowser)")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	u := BingSearchURL(query, opts)

	headers := ChromeHeaders()
	headers["referer"] = bingReferer
	headers["accept"] = acceptHTML

	data, _, status, err := b.browser.Do(http.MethodGet, u, headers, nil)
	if err != nil {
		return nil, fmt.Errorf("bing request: %w", err)
	}
	if isRateLimitStatus(status) {
		return nil, &ErrRateLimited{Engine: "bing"}
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("bing status %d", status)
	}
	if isBingRateLimited(data) {
		return nil, &ErrRateLimited{Engine: "bing"}
	}

	results, err := ParseBingHTML(data)
	if err != nil {
		return nil, fmt.Errorf("bing parse: %w", err)
	}

	slog.Debug("bing results", slog.Int("count", len(results)))
	return applyLimit(results, opts.Limit), nil
}

// ParseBingHTML extracts search results from Bing Search HTML response.
// Handles two Bing SERP layouts:
//   - Classic: h2 > a with href (direct or Bing redirect)
//   - New (ox-browser /fetch): a.tilk with RedirectUrl attr + cite tag for URL,
//     title in .b_tpcn text or tilk aria-label
func ParseBingHTML(data []byte) ([]Result, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(data)))
	if err != nil {
		return nil, fmt.Errorf("goquery parse: %w", err)
	}

	var results []Result

	doc.Find("#b_results > li.b_algo").Each(func(_ int, s *goquery.Selection) {
		// Primary path: h2 > a (classic Bing SERP layout)
		link := s.Find("h2 a").First()
		title := strings.TrimSpace(link.Text())
		href, exists := link.Attr("href")

		// Fallback: a.tilk with cite (new Bing SERP layout from ox-browser /fetch)
		if !exists || title == "" || href == "" {
			tilk := s.Find("a.tilk").First()
			href, exists = tilk.Attr("href")
			if !exists || href == "" {
				// Last resort: use cite text as URL
				citeText := strings.TrimSpace(s.Find("cite").First().Text())
				if citeText == "" {
					return
				}
				href = citeText
				if !strings.HasPrefix(href, "http") {
					href = "https://" + href
				}
			}
			if title == "" {
				title = strings.TrimSpace(tilk.AttrOr("aria-label", ""))
				if title == "" {
					title = strings.TrimSpace(s.Find("cite").First().Text())
				}
			}
		}

		if title == "" || href == "" {
			return
		}

		// Unwrap Bing redirect URLs: /ck/a?...&u=<base64url>&...
		href = bingUnwrapURL(href)

		snippet := strings.TrimSpace(
			s.Find(".b_caption p, p.b_lineclamp2, p.b_lineclamp3, p.b_lineclamp4").First().Text(),
		)

		results = append(results, Result{
			Title:    title,
			Content:  snippet,
			URL:      href,
			Score:    directResultScore,
			Metadata: map[string]string{"engine": "bing"},
		})
	})

	return results, nil
}

// bingUnwrapURL extracts the real URL from a Bing redirect link.
// Bing wraps URLs as /ck/a?...&u=<base64url-encoded-destination>&...
func bingUnwrapURL(rawURL string) string {
	if !strings.Contains(rawURL, "bing.com/ck/a") {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	encoded := u.Query().Get("u")
	if encoded == "" {
		return rawURL
	}
	// Bing uses base64url with a prefix (a1, a2, etc.) — strip the prefix.
	if len(encoded) > 2 && encoded[0] == 'a' && encoded[1] >= '0' && encoded[1] <= '9' {
		encoded = encoded[2:]
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return rawURL
	}
	return string(decoded)
}

// isBingRateLimited checks if Bing blocked the request.
func isBingRateLimited(body []byte) bool {
	lower := bytes.ToLower(body)
	markers := [][]byte{
		[]byte("captcha"),
		[]byte("rate limit"),
		[]byte("too many requests"),
		[]byte("unusual traffic"),
	}
	for _, m := range markers {
		if bytes.Contains(lower, m) {
			return true
		}
	}
	return false
}
