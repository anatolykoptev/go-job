package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
)

const yepEndpoint = "https://api.yep.com/fs/2/search"

// Yep searches Yep.com (Ahrefs) via its public JSON API.
// No API key required. Own independent index.
// Uses BrowserDoer (TLS fingerprint) to bypass Cloudflare protection.
type Yep struct {
	browser BrowserDoer
}

// YepOption configures Yep.
type YepOption func(*Yep)

// WithYepBrowser sets the BrowserDoer for HTTP requests.
func WithYepBrowser(bc BrowserDoer) YepOption {
	return func(y *Yep) { y.browser = bc }
}

// NewYep creates a Yep search client.
func NewYep(opts ...YepOption) *Yep {
	y := &Yep{}
	for _, o := range opts {
		o(y)
	}
	return y
}

// Search implements Provider. Queries Yep via JSON API.
func (y *Yep) Search(ctx context.Context, query string, opts SearchOpts) ([]Result, error) {
	if y.browser == nil {
		return nil, errors.New("yep: BrowserDoer is required (use WithYepBrowser)")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	args := url.Values{
		"client":     {"web"},
		"gl":         {"us"},
		"no_correct": {"false"},
		"q":          {query},
		"safeSearch": {"off"},
		"type":       {"web"},
	}

	u := yepEndpoint + "?" + args.Encode()
	headers := ChromeHeaders()
	headers["accept"] = "application/json"
	headers["referer"] = "https://yep.com/"

	data, _, status, err := y.browser.Do(http.MethodGet, u, headers, nil)
	if err != nil {
		return nil, fmt.Errorf("yep request: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("yep status %d", status)
	}

	results, err := ParseYepJSON(data)
	if err != nil {
		return nil, fmt.Errorf("yep parse: %w", err)
	}

	slog.Debug("yep results", slog.Int("count", len(results)))
	return applyLimit(results, opts.Limit), nil
}

// yepResponse is the top-level JSON response: ["Ok", {results: [...]}]
type yepResponse struct {
	Results []yepResult `json:"results"`
	Total   int         `json:"total"`
}

type yepResult struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
	Type    string `json:"type"` // "Organic", "Alt_search_engine"
}

// ParseYepJSON parses the Yep API response.
// Format: ["Ok", {"results": [...], "total": N}]
func ParseYepJSON(data []byte) ([]Result, error) {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal envelope: %w", err)
	}
	if len(raw) < 2 {
		return nil, fmt.Errorf("unexpected response length %d", len(raw))
	}

	var status string
	if err := json.Unmarshal(raw[0], &status); err != nil || status != "Ok" {
		return nil, fmt.Errorf("yep status: %s", string(raw[0]))
	}

	var body yepResponse
	if err := json.Unmarshal(raw[1], &body); err != nil {
		return nil, fmt.Errorf("unmarshal body: %w", err)
	}

	results := make([]Result, 0, len(body.Results))
	for _, r := range body.Results {
		if r.Type != "Organic" || r.URL == "" || r.Title == "" {
			continue
		}
		results = append(results, Result{
			Title:    r.Title,
			URL:      r.URL,
			Content:  r.Snippet,
			Score:    directResultScore,
			Metadata: map[string]string{"engine": "yep"},
		})
	}
	return results, nil
}
