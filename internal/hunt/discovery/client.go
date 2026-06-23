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
// The client returns []engine.SearxngResult so existing slug-regex callers in
// ats.go are unchanged.  GO_SEARCH_URL empty → the client is nil → discovery
// falls back to the existing local SearchDirect path (non-degrading).
package discovery

import (
	"bytes"
	"context"
	"encoding/json"
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

// Discoverer is the interface the ATS discovery seam depends on.
// The go-search-backed client and the nil-fallback both satisfy it.
type Discoverer interface {
	// DiscoverBoardURLs calls the remote search service with the given
	// site-scoped query and returns raw URL/title pairs for slug extraction.
	// Returns (nil, err) on timeout or HTTP error so callers can fall back.
	DiscoverBoardURLs(ctx context.Context, query string) ([]engine.SearxngResult, error)
}

// goSearchSource is the JSON shape returned by go-search's research tool.
// Only Sources is used; the LLM answer is ignored (we want raw URLs, not prose).
type goSearchSource struct {
	Index   int    `json:"index"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

type goSearchOutput struct {
	Sources []goSearchSource `json:"sources"`
}

// goSearchRequest mirrors go-search's SmartSearchInput for the fields we need.
type goSearchRequest struct {
	Query string `json:"query"`
	// depth=fast: snippets-only, no page fetch, no LLM synthesis.
	// go-search routes this through buildRawOutputFast → Sources populated,
	// LLM chain never called.  Confirmed by live probe 2026-06-23.
	Depth string `json:"depth"`
}

// mcpCallRequest wraps a tool call in the MCP JSONRPC envelope.
type mcpCallRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      int            `json:"id"`
	Method  string         `json:"method"`
	Params  mcpCallParams  `json:"params"`
}

type mcpCallParams struct {
	Name      string          `json:"name"`
	Arguments goSearchRequest `json:"arguments"`
}

// mcpCallResponse is the minimal response shape we decode.
type mcpCallResponse struct {
	Result struct {
		StructuredContent goSearchOutput `json:"structuredContent"`
		Content           []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
}

// Client calls go-search's research tool over HTTP MCP.
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

// DiscoverBoardURLs implements Discoverer by calling go-search research with
// depth=fast (no LLM, returns Sources[] immediately from snippet data).
// Returns (nil, err) on any transport or decode failure — callers fall back.
func (c *Client) DiscoverBoardURLs(ctx context.Context, query string) ([]engine.SearxngResult, error) {
	// Per-call budget on top of the parent ctx so we never block longer than
	// defaultDiscoveryTimeout regardless of the parent deadline.
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	body, err := json.Marshal(mcpCallRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
		Params: mcpCallParams{
			Name: "research",
			Arguments: goSearchRequest{
				Query: query,
				Depth: "fast",
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("discovery: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, c.baseURL+"/mcp", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("discovery: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("discovery: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discovery: go-search returned %d", resp.StatusCode)
	}

	// go-search may respond as SSE (text/event-stream) or plain JSON depending
	// on client Accept negotiation.  Strip the "data: " SSE prefix when present.
	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return nil, fmt.Errorf("discovery: read body: %w", err)
	}
	rawBody = stripSSEPrefix(rawBody)

	var out mcpCallResponse
	if err := json.Unmarshal(rawBody, &out); err != nil {
		return nil, fmt.Errorf("discovery: decode response: %w", err)
	}

	// Prefer structuredContent (richer, already parsed); fall back to
	// re-parsing the text content blob if structuredContent is empty.
	sources := out.Result.StructuredContent.Sources
	if len(sources) == 0 && len(out.Result.Content) > 0 {
		var fallback goSearchOutput
		if json.Unmarshal([]byte(out.Result.Content[0].Text), &fallback) == nil {
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

	slog.Debug("discovery: go-search sources", slog.String("query", query), slog.Int("count", len(results)))
	return results, nil
}

// stripSSEPrefix extracts the JSON payload from an SSE-framed body.
// SSE format: zero or more "field: value\n" lines followed by "\n".
// We scan line by line, skip non-data lines (event:, id:, retry:, comment :),
// and return the first "data: " line's value.  If no data: line is found the
// body is returned as-is (already plain JSON).
func stripSSEPrefix(b []byte) []byte {
	const dataPrefix = "data: "
	lines := bytes.Split(b, []byte("\n"))
	for _, line := range lines {
		line = bytes.TrimRight(line, "\r")
		if bytes.HasPrefix(line, []byte(dataPrefix)) {
			return bytes.TrimSpace(line[len(dataPrefix):])
		}
		// Skip SSE meta-lines (event:, id:, retry:, comment :).
		// An empty line terminates the SSE event — if we haven't found data: yet,
		// keep scanning in case of multi-event streams.
	}
	// No data: line found; return original trimmed bytes (already plain JSON).
	return bytes.TrimSpace(b)
}
