package websearch

import (
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
	startpageEndpoint   = "https://www.startpage.com/sp/search"
	startpageReferer    = "https://www.startpage.com/"
	startpageDefaultLang = "english"
)

// Startpage searches Startpage via POST form scraping.
type Startpage struct {
	browser  BrowserDoer
	language string
}

// StartpageOption configures Startpage.
type StartpageOption func(*Startpage)

// WithStartpageBrowser sets the BrowserDoer for HTTP requests.
func WithStartpageBrowser(bc BrowserDoer) StartpageOption {
	return func(sp *Startpage) { sp.browser = bc }
}

// WithStartpageLanguage sets the default search language (e.g. "english", "deutsch").
func WithStartpageLanguage(lang string) StartpageOption {
	return func(sp *Startpage) { sp.language = lang }
}

// NewStartpage creates a Startpage scraper. A BrowserDoer must be provided via WithStartpageBrowser.
func NewStartpage(opts ...StartpageOption) *Startpage {
	sp := &Startpage{language: startpageDefaultLang}
	for _, o := range opts {
		o(sp)
	}
	return sp
}

// Search implements Provider. Queries Startpage via POST form.
func (sp *Startpage) Search(ctx context.Context, query string, opts SearchOpts) ([]Result, error) {
	if sp.browser == nil {
		return nil, errors.New("startpage: BrowserDoer is required (use WithStartpageBrowser)")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	lang := sp.language
	if opts.Language != "" {
		lang = opts.Language
	}

	formBody := fmt.Sprintf("query=%s&cat=web&language=%s", url.QueryEscape(query), url.QueryEscape(lang))

	headers := ChromeHeaders()
	headers["referer"] = startpageReferer
	headers["content-type"] = "application/x-www-form-urlencoded"
	headers["accept"] = acceptHTML

	data, _, status, err := sp.browser.Do(http.MethodPost, startpageEndpoint, headers, strings.NewReader(formBody))
	if err != nil {
		return nil, fmt.Errorf("startpage request: %w", err)
	}
	if isRateLimitStatus(status) {
		return nil, &ErrRateLimited{Engine: "startpage"}
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("startpage status %d", status)
	}
	if isStartpageRateLimited(data) {
		return nil, &ErrRateLimited{Engine: "startpage"}
	}

	results, err := ParseStartpageHTML(data)
	if err != nil {
		return nil, fmt.Errorf("startpage parse: %w", err)
	}

	slog.Debug("startpage results", slog.Int("count", len(results)))
	return applyLimit(results, opts.Limit), nil
}

// ParseStartpageHTML extracts search results from Startpage HTML response.
func ParseStartpageHTML(data []byte) ([]Result, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(data)))
	if err != nil {
		return nil, fmt.Errorf("goquery parse: %w", err)
	}

	var results []Result

	doc.Find(".w-gl__result, .result").Each(func(_ int, s *goquery.Selection) {
		link := s.Find("a.w-gl__result-title, h3 a, a.result-link").First()
		title := strings.TrimSpace(link.Text())
		href, exists := link.Attr("href")
		if !exists || title == "" {
			return
		}

		desc := s.Find("p.w-gl__description, .w-gl__description, p.result-description").First()
		content := strings.TrimSpace(desc.Text())

		if href == "" || strings.Contains(href, "startpage.com/do/") {
			return
		}

		results = append(results, Result{
			Title:    title,
			Content:  content,
			URL:      href,
			Score:    directResultScore,
			Metadata: map[string]string{"engine": "startpage"},
		})
	})

	return results, nil
}
