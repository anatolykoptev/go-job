// Package fetch — Byparr fallback fetcher.
//
// When the primary proxy fails (403/connection error), fetches page content
// via a FlareSolverr-compatible API (Byparr/Camoufox). The solver returns
// full page HTML in solution.response, bypassing proxy blocks and CF challenges.
package fetch

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
)

const byparrTimeout = 90 * time.Second

// byparrRequest is the FlareSolverr v2 API request body.
type byparrRequest struct {
	Cmd        string `json:"cmd"`
	URL        string `json:"url"`
	MaxTimeout int    `json:"maxTimeout"`
}

// byparrResponse is the FlareSolverr v2 API response.
type byparrResponse struct {
	Status   string          `json:"status"`
	Message  string          `json:"message"`
	Solution *byparrSolution `json:"solution"`
}

type byparrSolution struct {
	URL      string `json:"url"`
	Status   int    `json:"status"`
	Response string `json:"response"`
}

// WithByparrFallback enables fallback to a Byparr/FlareSolverr API when proxy fails.
// The solver returns full page HTML, bypassing proxy blocks and CF challenges.
func WithByparrFallback(baseURL string) Option {
	return func(f *Fetcher) {
		if baseURL != "" {
			f.byparrURL = baseURL
		}
	}
}

// fetchViaByparr calls the Byparr/FlareSolverr API and returns page HTML as bytes.
func (f *Fetcher) fetchViaByparr(ctx context.Context, pageURL string) ([]byte, error) {
	body, err := json.Marshal(byparrRequest{
		Cmd:        "request.get",
		URL:        pageURL,
		MaxTimeout: int(byparrTimeout.Milliseconds()),
	})
	if err != nil {
		return nil, fmt.Errorf("byparr marshal: %w", err)
	}

	endpoint := f.byparrURL + "/v1"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("byparr request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: byparrTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("byparr call: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("byparr read: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("byparr HTTP %d: %s", resp.StatusCode, truncate(string(respBody)))
	}

	var result byparrResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("byparr parse: %w", err)
	}

	if result.Status != "ok" {
		return nil, fmt.Errorf("byparr error: %s", result.Message)
	}
	if result.Solution == nil || result.Solution.Response == "" {
		return nil, errors.New("byparr: empty response")
	}

	slog.Debug("byparr fallback ok",
		slog.String("url", pageURL),
		slog.Int("status", result.Solution.Status),
		slog.Int("size", len(result.Solution.Response)))

	return []byte(result.Solution.Response), nil
}

const maxTruncate = 200

func truncate(s string) string {
	if len(s) <= maxTruncate {
		return s
	}
	return s[:maxTruncate] + "..."
}
