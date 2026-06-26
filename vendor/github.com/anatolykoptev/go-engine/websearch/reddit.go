package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	redditEndpoint    = "https://www.reddit.com/search.json"
	redditBaseURL     = "https://www.reddit.com"
	redditMaxSelftext = 300
)

// Reddit searches Reddit via the public JSON API.
type Reddit struct {
	browser BrowserDoer
}

// RedditOption configures Reddit.
type RedditOption func(*Reddit)

// WithRedditBrowser sets the BrowserDoer for HTTP requests.
func WithRedditBrowser(bc BrowserDoer) RedditOption {
	return func(r *Reddit) { r.browser = bc }
}

// NewReddit creates a Reddit scraper. A BrowserDoer must be provided via WithRedditBrowser.
func NewReddit(opts ...RedditOption) *Reddit {
	r := &Reddit{}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Search implements Provider. Queries Reddit JSON API.
func (r *Reddit) Search(ctx context.Context, query string, opts SearchOpts) ([]Result, error) {
	if r.browser == nil {
		return nil, errors.New("reddit: BrowserDoer is required (use WithRedditBrowser)")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	u := redditEndpoint + "?q=" + url.QueryEscape(query) +
		"&limit=10&sort=relevance&t=all"

	headers := ChromeHeaders()
	headers["accept"] = acceptJSON

	data, _, status, err := r.browser.Do(http.MethodGet, u, headers, nil)
	if err != nil {
		return nil, fmt.Errorf("reddit request: %w", err)
	}
	if isRateLimitStatus(status) {
		return nil, &ErrRateLimited{Engine: "reddit"}
	}
	if isRedditRateLimited(data) {
		return nil, &ErrRateLimited{Engine: "reddit"}
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("reddit status %d", status)
	}

	results, err := ParseRedditJSON(data)
	if err != nil {
		return nil, fmt.Errorf("reddit parse: %w", err)
	}

	slog.Debug("reddit results", slog.Int("count", len(results)))
	return applyLimit(results, opts.Limit), nil
}

// redditListing mirrors Reddit's listing JSON structure.
type redditListing struct {
	Data struct {
		Children []struct {
			Data redditPost `json:"data"`
		} `json:"children"`
	} `json:"data"`
}

// redditPost holds fields from a single Reddit post.
type redditPost struct {
	Title       string `json:"title"`
	Permalink   string `json:"permalink"`
	Selftext    string `json:"selftext"`
	Score       int    `json:"score"`
	NumComments int    `json:"num_comments"`
	Subreddit   string `json:"subreddit"`
	URL         string `json:"url"`
}

// ParseRedditJSON extracts search results from Reddit JSON response.
func ParseRedditJSON(data []byte) ([]Result, error) {
	var listing redditListing
	if err := json.Unmarshal(data, &listing); err != nil {
		return nil, fmt.Errorf("json decode: %w", err)
	}

	results := make([]Result, 0, len(listing.Data.Children))
	for _, child := range listing.Data.Children {
		p := child.Data
		if p.Title == "" || p.Permalink == "" {
			continue
		}

		selftext := p.Selftext
		if len(selftext) > redditMaxSelftext {
			selftext = selftext[:redditMaxSelftext]
		}

		content := fmt.Sprintf("r/%s | %d pts | %d comments\n%s",
			p.Subreddit, p.Score, p.NumComments, strings.TrimSpace(selftext))

		results = append(results, Result{
			Title:   p.Title,
			URL:     redditBaseURL + p.Permalink,
			Content: strings.TrimSpace(content),
			Score:   directResultScore,
			Metadata: map[string]string{
				"engine":    "reddit",
				"subreddit": p.Subreddit,
			},
		})
	}
	return results, nil
}

// isRedditRateLimited checks for rate-limit indicators in Reddit JSON response.
func isRedditRateLimited(data []byte) bool {
	var errResp struct {
		Error   any    `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &errResp) != nil {
		return false
	}

	if v, ok := errResp.Error.(float64); ok && int(v) == http.StatusTooManyRequests {
		return true
	}

	return strings.EqualFold(errResp.Message, "Too Many Requests")
}

const (
	redditOAuthBase = "https://oauth.reddit.com"
)

// SearchOAuth searches Reddit via the OAuth API using a bearer token from tm.
// The OAuth endpoint (oauth.reddit.com) returns the same Listing/t3 JSON shape
// as the public API, so ParseRedditJSON is reused unchanged.
func SearchOAuth(ctx context.Context, doer BrowserDoer, tm RedditTokenManager, query, userAgent string) ([]Result, error) {
	tok, err := tm.Token(ctx, doer)
	if err != nil {
		return nil, fmt.Errorf("reddit oauth: token: %w", err)
	}

	u := redditOAuthBase + "/search?q=" + url.QueryEscape(query) +
		"&type=link&raw_json=1&limit=10&sort=relevance&t=all"

	headers := map[string]string{
		"Authorization": "Bearer " + tok,
		"User-Agent":    userAgent,
		"Accept":        acceptJSON,
	}

	data, respHeaders, status, err := doer.Do(http.MethodGet, u, headers, nil)
	if err != nil {
		return nil, fmt.Errorf("reddit oauth search: %w", err)
	}

	// Handle rate limiting: check both HTTP status and JSON body.
	if isRateLimitStatus(status) || isRedditRateLimited(data) {
		rl := &ErrRateLimited{Engine: "reddit-oauth"}
		if retryAfter, ok := respHeaders["retry-after"]; ok && retryAfter != "" {
			// best-effort parse; ignore if malformed
			if d, parseErr := time.ParseDuration(retryAfter + "s"); parseErr == nil {
				rl.RetryAfter = d
			}
		}
		return nil, rl
	}

	// 401 means the token expired; invalidate so next call refreshes.
	if status == http.StatusUnauthorized {
		tm.Invalidate()
		return nil, errors.New("reddit oauth: token expired (401) — invalidated, retry")
	}

	if status >= 500 {
		return nil, fmt.Errorf("reddit oauth search: status %d: %w", status, ErrTransient)
	}

	if status != http.StatusOK {
		return nil, fmt.Errorf("reddit oauth search: status %d", status)
	}

	results, err := ParseRedditJSON(data)
	if err != nil {
		return nil, fmt.Errorf("reddit oauth parse: %w", err)
	}

	return applyLimit(results, 0), nil
}
