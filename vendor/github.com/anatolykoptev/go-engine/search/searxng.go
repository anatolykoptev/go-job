package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/anatolykoptev/go-engine/fetch"
	"github.com/anatolykoptev/go-engine/metrics"
)

const metricSearchRequests = "search_requests"

// SearXNG queries a local SearXNG instance for search results.
type SearXNG struct {
	baseURL    string
	httpClient *http.Client
	metrics    *metrics.Registry
}

// SearXNGOption configures a SearXNG client.
type SearXNGOption func(*SearXNG)

// WithHTTPClient sets the HTTP client for SearXNG requests.
func WithHTTPClient(c *http.Client) SearXNGOption {
	return func(s *SearXNG) { s.httpClient = c }
}

// WithMetrics sets the metrics registry for tracking request counts.
func WithMetrics(m *metrics.Registry) SearXNGOption {
	return func(s *SearXNG) { s.metrics = m }
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

// Search queries SearXNG and returns results.
func (s *SearXNG) Search(ctx context.Context, query, language, timeRange, engines string) ([]Result, error) {
	u, err := url.Parse(s.baseURL + "/search")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("format", "json")
	if language != "" && language != "all" {
		q.Set("language", language)
	}
	if timeRange != "" {
		q.Set("time_range", timeRange)
	}
	if engines != "" {
		q.Set("engines", engines)
	}
	u.RawQuery = q.Encode()

	if s.metrics != nil {
		s.metrics.Incr(metricSearchRequests)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	// SearXNG botdetection requires X-Forwarded-For to identify the client IP.
	req.Header.Set("X-Forwarded-For", "127.0.0.1")

	resp, err := fetch.RetryHTTP(ctx, fetch.DefaultRetryConfig, func() (*http.Response, error) {
		return s.httpClient.Do(req) //nolint:bodyclose,gosec // closed below; URL is caller-provided
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var data searxngResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return data.Results, nil
}
