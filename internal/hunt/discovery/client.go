// Package discovery provides the cross-service URL-discovery client that
// delegates ATS board URL discovery to go-search's raw_web_search pipeline.
//
// Design (ADR-002-r1): raw_web_search is scrapers-only (Brave-API + DDG +
// ox-browser-search) — zero embedder involvement.  The previous delegation
// to /api/tools/research (depth=fast) still hit the full research pipeline
// including the ARM e5-large embedder; under concurrent ATS discovery load
// the embedder saturates and discovery times out.  raw_web_search bypasses
// the embedder entirely: live probe yields 42 greenhouse board URLs vs 2 from
// the research depth=fast path.
//
// Transport: go-search exposes its tools via a REST bridge at /api/tools/{name}
// (mcpserver.Config{RESTBridge:true}, defaultRESTPrefix="/api").  The REST
// bridge uses an in-process InMemoryTransport to the mcp.Server — it bypasses
// the StreamableHTTPHandler entirely and requires no Accept negotiation.
// Response envelope: {"content":[{"type":"text","text":"<inner-json>"}],"is_error":false}
// Inner JSON (raw_web_search): {"query":"...","results":[{"url","title","description","score"}],"total":N}
//
// DDG redirect handling: raw results may include DDG-wrapped URLs
// (duckduckgo.com/l/?uddg=<url-encoded-real-url>) and ad redirect URLs
// (duckduckgo.com/y.js?...).  The client unwraps the "uddg" query param and
// drops non-ATS-board URLs before returning results.
//
// The client returns []engine.SearxngResult so existing slug-regex callers in
// ats.go are unchanged.  GO_SEARCH_URL empty → the client is nil → discovery
// falls back to the existing local SearchDirect path (non-degrading).
package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// defaultDiscoveryTimeout is the per-call deadline for go-search round-trips.
//
// This value MUST exceed go-search's raw_web_search server-side ToolTimeout (90s,
// the go-mcpserver global default; raw_web_search has no per-tool override).  If
// go-job's context expires before go-search finishes, the HTTP call fails with a
// transport error and discoverJobURLs falls back with source="local-fallback",
// never seeing the 200+Degraded=true response that raw_web_search would return on
// partial fan-out failure.  Keeping go-job's budget above 90s lets the Degraded
// signal arrive so callers can emit the distinct source="degraded-fallback" label
// (separable from transport errors in dashboards/alerts).
//
// Current value: 90s server cap + 10s margin = 100s.
// The http.Client.Timeout is set to defaultDiscoveryTimeout + 2s as a safety net.
const defaultDiscoveryTimeout = 100 * time.Second

// restAPIPath is the REST bridge path for calling the raw_web_search tool.
// go-mcpserver defaults RESTPrefix to "/api"; go-search does not override it.
const restAPIPath = "/api/tools/raw_web_search"

// atsBoardHosts is the allowlist of ATS job-board hostnames.  raw_web_search
// results are filtered to this set after DDG-redirect unwrapping.  URLs on
// other hosts (ad redirects, tracking pixels, general web results) are dropped.
var atsBoardHosts = map[string]bool{ //nolint:gochecknoglobals // package-level constant set, never mutated
	"boards.greenhouse.io":     true,
	"job-boards.greenhouse.io": true,
	"jobs.lever.co":            true,
	"jobs.ashbyhq.com":         true,
}

// Discoverer is the interface the ATS discovery seam depends on.
// The go-search-backed client and the nil-fallback both satisfy it.
type Discoverer interface {
	// DiscoverBoardURLs calls the remote search service with the given
	// site-scoped query and returns raw URL/title pairs for slug extraction.
	// Returns (nil, err) on timeout or HTTP error so callers can fall back.
	DiscoverBoardURLs(ctx context.Context, query string) ([]engine.SearxngResult, error)
}

// RawSearcher is the interface for general-purpose web search via go-search.
// Unlike Discoverer, it does NOT filter results to ATS board hosts — it
// returns all raw_web_search results (DDG-redirect-unwrapped only).
// Used by person research and salary research as the primary search source,
// replacing the former SearXNG dependency.
type RawSearcher interface {
	// RawSearch calls go-search's raw_web_search with the given query and
	// returns all results (no host filtering). Returns (nil, err) on
	// transport/degraded errors so callers can fall back to SearchDirect.
	RawSearch(ctx context.Context, query string) ([]engine.SearxngResult, error)
}

// rawSearchRequest is the body sent to POST /api/tools/raw_web_search.
type rawSearchRequest struct {
	Query string `json:"query"`
}

// rawSearchResult is one hit from the raw_web_search tool.
type rawSearchResult struct {
	URL         string  `json:"url"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Score       float64 `json:"score"`
}

// rawSearchOutput is the inner JSON object carried in content[0].text for
// the raw_web_search tool.
//
// Degraded and DegradeReason are the in-band failure signal introduced in
// go-search P3.  When Degraded is true the result set cannot be trusted (all
// broad-web legs failed or the context deadline fired) and callers MUST fall
// back to local scrapers rather than treating it as a clean zero.
//
// Backward compatibility: older go-search versions do not emit the "degraded"
// field.  JSON absent → Go zero-value → Degraded=false → treated as healthy.
type rawSearchOutput struct {
	Results       []rawSearchResult `json:"results"`
	Total         int               `json:"total"`
	Degraded      bool              `json:"degraded"`
	DegradeReason string            `json:"degrade_reason,omitempty"`
}

// rawSearchEnvelope is the REST bridge envelope returned by POST /api/tools/raw_web_search.
// Results are encoded as a JSON string in content[0].text (no "structured" field).
//
//	{"content":[{"type":"text","text":"<rawSearchOutput-json>"}],"is_error":false}
type rawSearchEnvelope struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"is_error"`
}

// Client calls go-search's raw_web_search tool over the REST bridge (plain
// HTTP+JSON, no MCP envelope, no SSE, no Accept negotiation).
type Client struct {
	baseURL string
	http    *http.Client
	timeout time.Duration
}

// NewClient constructs a Client targeting the given go-search base URL.
// baseURL should be e.g. "http://10.9.0.10:8890".
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		http:    &http.Client{Timeout: defaultDiscoveryTimeout + 2*time.Second},
		timeout: defaultDiscoveryTimeout,
	}
}

// DiscoverBoardURLs implements Discoverer by calling go-search's raw_web_search
// REST bridge (scrapers-only, zero embedder).  Raw results are DDG-redirect
// unwrapped and filtered to ATS board hosts before being returned.
// Returns (nil, err) on any transport or decode failure — callers fall back to
// local SearchDirect.
func (c *Client) DiscoverBoardURLs(ctx context.Context, query string) ([]engine.SearxngResult, error) {
	out, err := c.callRawWebSearch(ctx, query)
	if err != nil {
		return nil, err
	}

	// Clean zero: go-search answered with no results. Return nil,nil —
	// caller treats this as authoritative empty (no local fallback).
	if len(out.Results) == 0 {
		return nil, nil
	}

	results := make([]engine.SearxngResult, 0, len(out.Results))
	nonEmptyURLCount := 0

	for _, r := range out.Results {
		if r.URL == "" {
			continue
		}
		nonEmptyURLCount++

		clean, ok := unwrapDDG(r.URL)
		if !ok {
			continue
		}
		if !isATSBoardHost(clean) {
			continue
		}
		results = append(results, engine.SearxngResult{
			URL:     clean,
			Title:   r.Title,
			Content: r.Description,
		})
	}

	// Schema-drift guard: results present but ALL have empty URL.
	if nonEmptyURLCount == 0 {
		return nil, fmt.Errorf("discovery: raw_web_search returned %d result(s) but all had empty URL (malformed response)", len(out.Results))
	}

	if len(results) == 0 {
		slog.Debug("discovery: no ATS board URLs after filtering",
			slog.String("query", query), slog.Int("raw", len(out.Results)))
		return nil, nil
	}

	slog.Debug("discovery: raw_web_search results",
		slog.String("query", query),
		slog.Int("raw", len(out.Results)),
		slog.Int("board", len(results)))
	return results, nil
}

// RawSearch implements RawSearcher by calling go-search's raw_web_search
// REST bridge WITHOUT ATS board host filtering. Results are DDG-redirect
// unwrapped only — all web results are returned. Used by person research
// and salary research as the primary search source (replacing SearXNG).
// Returns (nil, err) on transport/degraded errors so callers fall back
// to engine.SearchDirect.
func (c *Client) RawSearch(ctx context.Context, query string) ([]engine.SearxngResult, error) {
	out, err := c.callRawWebSearch(ctx, query)
	if err != nil {
		return nil, err
	}

	results := make([]engine.SearxngResult, 0, len(out.Results))
	for _, r := range out.Results {
		if r.URL == "" {
			continue
		}
		clean, ok := unwrapDDG(r.URL)
		if !ok {
			continue
		}
		results = append(results, engine.SearxngResult{
			URL:     clean,
			Title:   r.Title,
			Content: r.Description,
		})
	}

	slog.Debug("discovery: raw_web_search (unfiltered)",
		slog.String("query", query),
		slog.Int("results", len(results)))
	return results, nil
}

// callRawWebSearch is the shared HTTP round-trip to go-search's raw_web_search
// REST bridge. Handles envelope decoding, degraded signal, and clean-zero
// detection. Returns the raw output for caller-specific post-processing
// (ATS host filtering for DiscoverBoardURLs, no filtering for RawSearch).
func (c *Client) callRawWebSearch(ctx context.Context, query string) (*rawSearchOutput, error) {
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	body, err := json.Marshal(rawSearchRequest{Query: query})
	if err != nil {
		return nil, fmt.Errorf("discovery: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, c.baseURL+restAPIPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("discovery: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("discovery: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discovery: go-search returned %d", resp.StatusCode)
	}

	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return nil, fmt.Errorf("discovery: read body: %w", err)
	}

	var envelope rawSearchEnvelope
	if err := json.Unmarshal(rawBody, &envelope); err != nil {
		return nil, fmt.Errorf("discovery: decode envelope: %w", err)
	}

	if envelope.IsError {
		return nil, errors.New("discovery: go-search reported is_error=true")
	}

	if len(envelope.Content) == 0 {
		slog.Debug("discovery: go-search returned empty content", slog.String("query", query))
		return &rawSearchOutput{}, nil
	}

	var out rawSearchOutput
	if err := json.Unmarshal([]byte(envelope.Content[0].Text), &out); err != nil {
		return nil, fmt.Errorf("discovery: decode raw_web_search output: %w", err)
	}

	if out.Degraded {
		// Return an error wrapping ErrDiscoveryDegraded so callers (ats.discoverJobURLs)
		// can errors.Is-detect it and emit the distinct source="degraded-fallback"
		// metric label. The caller logs the WARN at the layer that handles the
		// fallback — logging here too would duplicate the line. The reason is
		// embedded in the error message (not duplicated from the base error).
		return nil, fmt.Errorf("discovery: %s: %w", out.DegradeReason, engine.ErrDiscoveryDegraded)
	}

	if len(out.Results) == 0 {
		slog.Debug("discovery: go-search returned no results", slog.String("query", query))
		return &rawSearchOutput{}, nil
	}

	return &out, nil
}

// unwrapDDG handles DDG redirect URLs of the form:
//
//	https://duckduckgo.com/l/?uddg=<url-encoded-real-url>&...
//
// Returns (rawURL, true) when the URL is not on the duckduckgo.com domain, or
// when it is a valid DDG redirect (the decoded real URL).  Returns ("", false)
// for DDG ad/tracking URLs that carry no real destination (e.g. y.js).
func unwrapDDG(rawURL string) (string, bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		// Unparseable URL — pass through; the host filter will drop it.
		return rawURL, true
	}
	if parsed.Hostname() != "duckduckgo.com" {
		return rawURL, true
	}
	// DDG redirect: /l/ path with uddg param carries the real destination.
	if uddg := parsed.Query().Get("uddg"); uddg != "" {
		// Query().Get() already URL-decodes the param value.
		return uddg, true
	}
	// Any other duckduckgo.com URL (ads, y.js click-tracking, etc.) — drop.
	return "", false
}

// isATSBoardHost reports whether the given URL's host is in atsBoardHosts.
func isATSBoardHost(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return atsBoardHosts[parsed.Hostname()]
}
