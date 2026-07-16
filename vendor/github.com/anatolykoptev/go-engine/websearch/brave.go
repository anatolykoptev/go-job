//nolint:goconst,dupl
package websearch

import (
	"bytes"
	"context"
	"errors"
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

// BraveSearchURL returns the GET URL for the Brave Search HTML SERP endpoint.
// P2 feeds this URL to ox-browser /fetch so the SERP request shape stays
// single-owned in websearch (ADR-8: const host + url.QueryEscape only).
func BraveSearchURL(query string, opts ...SearchOpts) string {
	u := braveEndpoint + "?q=" + url.QueryEscape(query) + "&source=web"
	if len(opts) > 0 {
		if tf := timeRangeToBrave(opts[0].TimeRange); tf != "" {
			u += "&tf=" + tf
		}
	}
	return u
}

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
		return nil, errors.New("brave: BrowserDoer is required (use WithBraveBrowser)")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	u := BraveSearchURL(query, opts)

	headers := ChromeHeaders()
	headers["referer"] = braveReferer
	headers["accept"] = acceptHTML

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
// Context-aware: "captcha" appears in Brave's i18n translation JSON
// (e.g. "Switch to traditional captcha":"Switch to traditional CAPTCHA") even
// on normal result pages. We distinguish i18n context (key":"value) from a
// real captcha page where "captcha" appears in plain HTML text.
func isBraveRateLimited(body []byte) bool {
	lower := bytes.ToLower(body)

	// Strong markers — always indicate rate limiting
	strongMarkers := [][]byte{
		[]byte("rate limit"),
		[]byte("too many requests"),
		[]byte("unusual traffic"),
		[]byte("are you a robot"),
		[]byte("please verify you are human"),
		[]byte("please solve the captcha"),
	}
	for _, m := range strongMarkers {
		if bytes.Contains(lower, m) {
			return true
		}
	}

	// Weak marker "captcha" — only counts if NOT in i18n JSON context.
	// Brave's i18n strings look like: "captcha":"Switch to traditional CAPTCHA"
	// A real captcha page has "captcha" in plain HTML text, not JSON.
	if bytes.Contains(lower, []byte("captcha")) {
		if !isCaptchaInI18nContext(lower) {
			return true
		}
	}

	return false
}

// isCaptchaInI18nContext checks if ALL occurrences of "captcha" in the body
// are inside i18n JSON strings (key":"value pattern). If at least one
// occurrence is in plain HTML context, returns false (real captcha page).
func isCaptchaInI18nContext(lower []byte) bool {
	idx := 0
	for {
		pos := bytes.Index(lower[idx:], []byte("captcha"))
		if pos < 0 {
			break
		}
		pos += idx
		// Check surrounding context for JSON key-value pattern
		start := max(0, pos-20)
		end := min(len(lower), pos+30)
		context := lower[start:end]
		// i18n pattern: "captcha":"... or "captcha": "...
		if !bytes.Contains(context, []byte(`":"`)) && !bytes.Contains(context, []byte(`": "`)) {
			return false // found captcha in non-JSON context
		}
		idx = pos + len("captcha")
	}
	return true
}
