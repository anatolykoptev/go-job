package websearch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const (
	ddgDefaultRegion = "wt-wt"
	ddgHTMLEndpoint  = "https://html.duckduckgo.com/html/"
	ddgDJSEndpoint   = "https://links.duckduckgo.com/d.js"
	ddgHomepage      = "https://duckduckgo.com/"
	directResultScore = 1.0
)

var vqdPatterns = []*regexp.Regexp{
	regexp.MustCompile(`vqd='([^']+)'`),
	regexp.MustCompile(`vqd="([^"]+)"`),
	regexp.MustCompile(`vqd=([a-zA-Z0-9_-]+)`),
}

// DDG searches DuckDuckGo using HTML lite + d.js fallback.
type DDG struct {
	browser BrowserDoer
	region  string
}

// DDGOption configures DDG.
type DDGOption func(*DDG)

// WithDDGBrowser sets the BrowserDoer for HTTP requests.
func WithDDGBrowser(bc BrowserDoer) DDGOption {
	return func(d *DDG) { d.browser = bc }
}

// WithDDGRegion sets the region code (e.g. "wt-wt", "ru-ru").
func WithDDGRegion(region string) DDGOption {
	return func(d *DDG) { d.region = region }
}

// NewDDG creates a DuckDuckGo scraper. A BrowserDoer must be provided via WithDDGBrowser.
func NewDDG(opts ...DDGOption) (*DDG, error) {
	d := &DDG{region: ddgDefaultRegion}
	for _, o := range opts {
		o(d)
	}
	if d.browser == nil {
		return nil, errors.New("ddg: BrowserDoer is required (use WithDDGBrowser)")
	}
	return d, nil
}

// Search implements Provider. Primary: HTML lite, fallback: d.js JSON API.
func (d *DDG) Search(ctx context.Context, query string, opts SearchOpts) ([]Result, error) {
	region := d.region
	if opts.Region != "" {
		region = opts.Region
	}

	results, err := d.searchHTML(ctx, query, region)
	if err == nil && len(results) > 0 {
		slog.Debug("ddg results (html)", slog.Int("count", len(results)))
		return applyLimit(results, opts.Limit), nil
	}
	if err != nil {
		slog.Debug("ddg html failed, trying d.js", slog.Any("error", err))
	}

	vqd, err := d.getVQD(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("ddg vqd: %w", err)
	}

	results, err = d.searchDJS(ctx, query, vqd, region)
	if err != nil {
		return nil, fmt.Errorf("ddg d.js: %w", err)
	}

	slog.Debug("ddg results (d.js)", slog.Int("count", len(results)))
	return applyLimit(results, opts.Limit), nil
}

func (d *DDG) searchHTML(ctx context.Context, query, region string) ([]Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	formBody := fmt.Sprintf("q=%s&kl=%s&df=", url.QueryEscape(query), url.QueryEscape(region))

	headers := ChromeHeaders()
	headers["referer"] = "https://html.duckduckgo.com/"
	headers["content-type"] = "application/x-www-form-urlencoded"

	data, _, status, err := d.browser.Do(http.MethodPost, ddgHTMLEndpoint, headers, strings.NewReader(formBody))
	if err != nil {
		return nil, err
	}
	if isRateLimitStatus(status) {
		return nil, &ErrRateLimited{Engine: "ddg"}
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("ddg html status %d", status)
	}
	if isDDGRateLimited(data) {
		return nil, &ErrRateLimited{Engine: "ddg"}
	}

	return ParseDDGHTML(data)
}

func (d *DDG) getVQD(ctx context.Context, query string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	u := ddgHomepage + "?q=" + url.QueryEscape(query)

	headers := ChromeHeaders()
	headers["referer"] = ddgHomepage

	data, _, status, err := d.browser.Do(http.MethodGet, u, headers, nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("ddg homepage status %d", status)
	}

	if vqd := ExtractVQD(string(data)); vqd != "" {
		return vqd, nil
	}
	return "", fmt.Errorf("vqd token not found (%d bytes)", len(data))
}

func (d *DDG) searchDJS(ctx context.Context, query, vqd, region string) ([]Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	params := url.Values{
		"q": {query}, "vqd": {vqd}, "kl": {region},
		"df": {""}, "l": {"us-en"}, "o": {"json"},
	}
	u := ddgDJSEndpoint + "?" + params.Encode()

	headers := ChromeHeaders()
	headers["referer"] = ddgHomepage
	headers["accept"] = "application/json, text/javascript, */*; q=0.01"

	data, _, status, err := d.browser.Do(http.MethodGet, u, headers, nil)
	if err != nil {
		return nil, err
	}
	if isRateLimitStatus(status) {
		return nil, &ErrRateLimited{Engine: "ddg"}
	}
	if status != http.StatusOK && status != http.StatusAccepted {
		return nil, fmt.Errorf("ddg d.js status %d", status)
	}

	return ParseDDGResponse(data)
}

func applyLimit(results []Result, limit int) []Result {
	if limit > 0 && len(results) > limit {
		return results[:limit]
	}
	return results
}
