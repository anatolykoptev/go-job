package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

const languageAll = "all"

// searxngRawResult is a raw SearXNG result with flexible Metadata type.
// SearXNG may return metadata as either a map or an empty string.
type searxngRawResult struct {
	Title    string          `json:"title"`
	URL      string          `json:"url"`
	Content  string          `json:"content"`
	Score    float64         `json:"score"`
	Engines  []string        `json:"engines"`
	Metadata json.RawMessage `json:"metadata"`
}

// searxngResponse is the JSON response from SearXNG API.
type searxngResponse struct {
	Results []searxngRawResult `json:"results"`
}

// toResults converts raw SearXNG results to websearch.Result,
// gracefully handling metadata that may be a string or map.
func (r *searxngResponse) toResults() []Result {
	out := make([]Result, len(r.Results))
	for i, sr := range r.Results {
		out[i] = Result{
			Title:   sr.Title,
			URL:     sr.URL,
			Content: sr.Content,
			Score:   sr.Score,
		}
		if len(sr.Metadata) > 0 && sr.Metadata[0] == '{' {
			_ = json.Unmarshal(sr.Metadata, &out[i].Metadata)
		}
	}
	return out
}

// SearXNG queries a local SearXNG instance for search results.
type SearXNG struct {
	baseURL    string
	httpClient *http.Client
	maxResults int
}

// SearXNGOption configures a SearXNG client.
type SearXNGOption func(*SearXNG)

// WithSearXNGHTTPClient sets the HTTP client for SearXNG requests.
func WithSearXNGHTTPClient(c *http.Client) SearXNGOption {
	return func(s *SearXNG) { s.httpClient = c }
}

// WithSearXNGMaxResults sets the maximum number of results to return.
func WithSearXNGMaxResults(n int) SearXNGOption {
	return func(s *SearXNG) { s.maxResults = n }
}

// NewSearXNG creates a SearXNG client.
func NewSearXNG(baseURL string, opts ...SearXNGOption) *SearXNG {
	s := &SearXNG{
		baseURL:    baseURL,
		httpClient: http.DefaultClient,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Search implements Provider. Queries SearXNG and returns results.
func (s *SearXNG) Search(ctx context.Context, query string, opts SearchOpts) ([]Result, error) {
	results, err := s.SearchAdvanced(ctx, query, opts.Language, opts.TimeRange, opts.Engines, "")
	if err != nil {
		return nil, err
	}
	limit := opts.Limit
	if limit == 0 {
		limit = s.maxResults
	}
	return applyLimit(results, limit), nil
}

// SearchAdvanced queries SearXNG with full control over parameters.
func (s *SearXNG) SearchAdvanced(
	ctx context.Context, query, language, timeRange, engines, categories string,
) ([]Result, error) {
	u, err := url.Parse(s.baseURL + "/search")
	if err != nil {
		return nil, fmt.Errorf("searxng: %w", err)
	}

	q := u.Query()
	q.Set("q", query)
	q.Set("format", "json")
	if language != "" && language != languageAll {
		q.Set("language", language)
	}
	if timeRange != "" {
		q.Set("time_range", timeRange)
	}
	if engines != "" {
		q.Set("engines", engines)
	}
	if categories != "" {
		q.Set("categories", categories)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("searxng request: %w", err)
	}
	// SearXNG botdetection requires X-Forwarded-For to identify the client IP.
	req.Header.Set("X-Forwarded-For", "127.0.0.1")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("searxng http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("searxng HTTP %d: %s", resp.StatusCode, string(body))
	}

	var data searxngResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("searxng json: %w", err)
	}
	return data.toResults(), nil
}
