package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/time/rate"

	"github.com/anatolykoptev/go-engine/metrics"
	"github.com/anatolykoptev/go-engine/sources"
)

const (
	metricBraveAPIRequests = "brave_api_requests"
	braveAPIEndpoint       = "https://api.search.brave.com/res/v1/web/search"

	// braveAPIScore is the default relevance score assigned to Brave API results.
	// Brave API returns JSON without a numeric relevance score; we assign a
	// mid-range value so RRF-based fusion treats these as decent-quality results
	// without over-weighting them compared to sources that do emit scores.
	braveAPIScore = 0.5
)

// braveAPILimiter enforces the Brave free tier: 1 q/s, burst 1.
// Stateless within the client; the caller (go-search) gates spend separately.
var braveAPILimiter = rate.NewLimiter(1, 1)

// braveAPIHTTPClient is the HTTP client used by braveAPIFetch.
// Package-level var so tests can substitute a test-server-backed transport.
var braveAPIHTTPClient = &http.Client{}

// braveAPIResponse is the top-level JSON from Brave Search API.
type braveAPIResponse struct {
	Web struct {
		Results []braveAPIResult `json:"results"`
	} `json:"web"`
}

type braveAPIResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

// SearchBraveAPI queries the Brave Search API (keyed, JSON, not the scraper).
// It is STATELESS: it does not check spend caps or Redis — the caller must do that.
// Label "brave_api" is distinct from the scraper label "brave".
// 429 → outcome="fail", nil returned (no retry-into-spend).
func SearchBraveAPI(ctx context.Context, apiKey, query string, m *metrics.Registry) ([]sources.Result, error) {
	if apiKey == "" {
		return nil, nil
	}
	if err := braveAPILimiter.Wait(ctx); err != nil {
		slog.Debug("brave_api rate limit wait cancelled", slog.Any("error", err))
		incrSourceResult(m, "brave_api", "fail")
		return nil, nil //nolint:nilerr // context cancelled: skip
	}

	params := url.Values{
		"q":     {query},
		"count": {"10"},
	}
	reqURL := braveAPIEndpoint + "?" + params.Encode()
	headers := map[string]string{
		"X-Subscription-Token": apiKey,
		"Accept":               "application/json",
	}

	body, err := braveAPIFetch(ctx, reqURL, headers)
	if err != nil {
		slog.Warn("brave_api fetch error", slog.Any("error", err))
		incrSourceResult(m, "brave_api", "fail")
		return nil, nil //nolint:nilerr // non-fatal: escalation just skips
	}

	var resp braveAPIResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		slog.Warn("brave_api json parse error", slog.Any("error", err))
		incrSourceResult(m, "brave_api", "fail")
		return nil, nil //nolint:nilerr
	}

	results := make([]sources.Result, 0, len(resp.Web.Results))
	for _, r := range resp.Web.Results {
		if r.URL == "" || r.Title == "" {
			continue
		}
		results = append(results, sources.Result{
			Title:    r.Title,
			URL:      r.URL,
			Content:  strings.TrimSpace(r.Description),
			Score:    braveAPIScore,
			Metadata: map[string]string{"engine": "brave_api"},
		})
	}

	outcome := "ok"
	if len(results) == 0 {
		outcome = "fail"
	}
	incrSourceResult(m, "brave_api", outcome)
	slog.Info("brave_api results", slog.String("query", query), slog.Int("count", len(results)))
	return results, nil
}

// braveAPIFetch performs a plain HTTPS GET against the Brave Search API.
// Not browser-fingerprinted — Brave API accepts standard TLS from any client.
func braveAPIFetch(ctx context.Context, reqURL string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := braveAPIHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, errors.New("brave_api 429 rate limited")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("brave_api HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// incrSourceResult records a per-source result counter.
// Key format matches the Prometheus label emitted by the gosearch registry:
// gosearch_go_search_source_result_total{source=X,outcome=Y}
func incrSourceResult(m *metrics.Registry, source, outcome string) {
	if m == nil {
		return
	}
	m.Incr(fmt.Sprintf("go_search_source_result_total{source=%s,outcome=%s}", source, outcome))
}
