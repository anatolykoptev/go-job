// Package discovery provides the cross-service URL-discovery client that
// delegates ATS board URL discovery to go-search's fused multi-source pipeline.
//
// Design (ADR-002): go-job's local SearchDirect covers only DDG/Marginalia
// which are unreliable from the Oracle DC ASN.  go-search's research tool adds
// Brave-API + ox-browser-search + RRF fusion — the only DC-reliable sources
// that index boards.greenhouse.io, jobs.lever.co, and jobs.ashbyhq.com.
// Reproducing those legs in go-job would duplicate a paid-API budget and a
// headless pool; delegating keeps them single-metered.
//
// Transport: go-search exposes its tools via a REST bridge at /api/tools/{name}
// (mcpserver.Config{RESTBridge:true}, defaultRESTPrefix="/api").  The REST
// bridge uses an in-process InMemoryTransport to the mcp.Server — it bypasses
// the StreamableHTTPHandler entirely and requires no Accept negotiation.
// Response is plain JSON: {"content":[{"type":"text","text":"<json>"}],
// "structured":{...sources[]},"is_error":false}.
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
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// defaultDiscoveryTimeout is the per-call deadline for go-search round-trips.
// Must stay well under the ToolTimeout default (90s) — discovery is one of
// several substeps in SearchGreenhouseJobs/Lever/Ashby.
const defaultDiscoveryTimeout = 20 * time.Second

// restAPIPath is the REST bridge path for calling the research tool.
// go-mcpserver defaults RESTPrefix to "/api"; go-search does not override it.
const restAPIPath = "/api/tools/research"

// Discoverer is the interface the ATS discovery seam depends on.
// The go-search-backed client and the nil-fallback both satisfy it.
type Discoverer interface {
	// DiscoverBoardURLs calls the remote search service with the given
	// site-scoped query and returns raw URL/title pairs for slug extraction.
	// Returns (nil, err) on timeout or HTTP error so callers can fall back.
	DiscoverBoardURLs(ctx context.Context, query string) ([]engine.SearxngResult, error)
}

// restSource is the JSON shape of one source entry returned by go-search's
// REST bridge for the research tool.
type restSource struct {
	Index   int    `json:"index"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// restOutput is the "structured" field of the REST bridge response.
type restOutput struct {
	Sources []restSource `json:"sources"`
}

// restResponse is the envelope returned by POST /api/tools/research.
// go-mcpserver REST bridge always returns:
//
//	{"content":[{"type":"text","text":"<inner-json>"}],
//	 "structured":{<restOutput>},"is_error":false}
type restResponse struct {
	// Structured carries the parsed research output including sources.
	Structured restOutput `json:"structured"`
	// Content carries the text blob for fallback parsing when Structured.Sources is empty.
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"is_error"`
}

// restRequest is the body sent to POST /api/tools/research.
type restRequest struct {
	Query string `json:"query"`
	// Depth=fast: snippets-only path in go-search, no page fetch, no LLM
	// synthesis.  Sources[] populated immediately from search result snippets.
	// Confirmed by live probe 2026-06-23.
	Depth string `json:"depth"`
	// BoardDiscovery instructs go-search to relax the per-domain dedup cap so
	// many results from the same ATS board host (jobs.lever.co, boards.greenhouse.io,
	// jobs.ashbyhq.com) survive into the result pool instead of being capped at 2.
	// Added in go-search PR #65 (board_discovery JSON key, omitempty on their side).
	// Inert / ignored by older go-search versions — backward-compatible.
	BoardDiscovery bool `json:"board_discovery,omitempty"`
}

// Client calls go-search's research tool over the REST bridge (plain HTTP+JSON,
// no MCP envelope, no SSE, no Accept negotiation).
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

// DiscoverBoardURLs implements Discoverer by calling go-search's REST bridge
// at POST /api/tools/research with depth=fast (snippets-only, no LLM).
// Returns (nil, err) on any transport or decode failure — callers fall back.
func (c *Client) DiscoverBoardURLs(ctx context.Context, query string) ([]engine.SearxngResult, error) {
	// Per-call budget on top of the parent ctx so we never block longer than
	// defaultDiscoveryTimeout regardless of the parent deadline.
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	body, err := json.Marshal(restRequest{
		Query:          query,
		Depth:          "fast",
		BoardDiscovery: true,
	})
	if err != nil {
		return nil, fmt.Errorf("discovery: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, c.baseURL+restAPIPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("discovery: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// REST bridge uses an in-process InMemoryTransport — no StreamableHTTP
	// Accept negotiation required (unlike POST /mcp which requires both
	// "application/json" and "text/event-stream" per go-sdk streamable.go:284).

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

	var out restResponse
	if err := json.Unmarshal(rawBody, &out); err != nil {
		return nil, fmt.Errorf("discovery: decode response: %w", err)
	}

	if out.IsError {
		return nil, errors.New("discovery: go-search reported is_error=true")
	}

	// Prefer structured.sources (already parsed); fall back to re-parsing
	// content[0].text as a restOutput JSON blob.
	sources := out.Structured.Sources
	if len(sources) == 0 && len(out.Content) > 0 {
		var fallback restOutput
		if json.Unmarshal([]byte(out.Content[0].Text), &fallback) == nil {
			sources = fallback.Sources
		}
	}

	if len(sources) == 0 {
		slog.Debug("discovery: go-search returned no sources", slog.String("query", query))
		return nil, nil
	}

	results := make([]engine.SearxngResult, 0, len(sources))
	for _, s := range sources {
		if s.URL == "" {
			continue
		}
		results = append(results, engine.SearxngResult{
			URL:     s.URL,
			Title:   s.Title,
			Content: s.Snippet,
		})
	}

	// If go-search returned sources but every source has an empty URL, treat it
	// as a malformed response rather than a genuine "nothing found" answer.
	// Returning an error lets the caller fall back to local scrapers, which is
	// safer than short-circuiting on a response that may just be schema drift.
	if len(results) == 0 && len(sources) > 0 {
		return nil, fmt.Errorf("discovery: go-search returned %d source(s) but all had empty URL (malformed response)", len(sources))
	}

	slog.Debug("discovery: go-search sources", slog.String("query", query), slog.Int("count", len(results)))
	return results, nil
}
