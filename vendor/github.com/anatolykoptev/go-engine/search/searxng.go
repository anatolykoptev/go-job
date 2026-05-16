package search

import (
	"context"
	"net/http"

	"github.com/anatolykoptev/go-engine/metrics"
	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-engine/websearch"
)

const (
	metricSearchRequests = "search_requests_total"
	metricSearchErrors   = "search_errors_total"
)

// SearXNG queries a local SearXNG instance for search results.
// Wraps websearch.SearXNG with go-engine metrics support.
type SearXNG struct {
	inner   *websearch.SearXNG
	metrics *metrics.Registry
}

// SearXNGOption configures a SearXNG client.
type SearXNGOption func(*searxngConfig)

type searxngConfig struct {
	httpClient *http.Client
	metrics    *metrics.Registry
}

// WithHTTPClient sets the HTTP client for SearXNG requests.
func WithHTTPClient(c *http.Client) SearXNGOption {
	return func(cfg *searxngConfig) { cfg.httpClient = c }
}

// WithMetrics sets the metrics registry for tracking request counts.
func WithMetrics(m *metrics.Registry) SearXNGOption {
	return func(cfg *searxngConfig) { cfg.metrics = m }
}

// NewSearXNG creates a SearXNG client.
func NewSearXNG(baseURL string, opts ...SearXNGOption) *SearXNG {
	cfg := &searxngConfig{}
	for _, o := range opts {
		o(cfg)
	}
	var wsOpts []websearch.SearXNGOption
	if cfg.httpClient != nil {
		wsOpts = append(wsOpts, websearch.WithSearXNGHTTPClient(cfg.httpClient))
	}
	return &SearXNG{
		inner:   websearch.NewSearXNG(baseURL, wsOpts...),
		metrics: cfg.metrics,
	}
}

// SearchQuery queries SearXNG using a sources.Query.
// Reads Extra["categories"] and Extra["engines"] if set.
func (s *SearXNG) SearchQuery(ctx context.Context, q sources.Query) ([]sources.Result, error) {
	if s.metrics != nil {
		s.metrics.Incr(metricSearchRequests)
	}
	engines := q.Extra["engines"]
	categories := q.Extra["categories"]
	ws, err := s.inner.SearchAdvanced(ctx, q.Text, q.Language, q.TimeRange, engines, categories)
	if err != nil {
		return nil, err
	}
	return ws, nil
}

// Search queries SearXNG and returns results.
func (s *SearXNG) Search(ctx context.Context, query, language, timeRange, engines string) ([]sources.Result, error) {
	if s.metrics != nil {
		s.metrics.Incr(metricSearchRequests)
	}
	ws, err := s.inner.SearchAdvanced(ctx, query, language, timeRange, engines, "")
	if err != nil {
		return nil, err
	}
	return ws, nil
}
