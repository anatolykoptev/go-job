// Package fetch — go-browser /render fallback fetcher.
//
// When the primary proxy and ox-browser fail, fetches fully rendered HTML
// via go-browser's /render endpoint. Uses CloakBrowser for full JS execution
// with stealth fingerprinting, replacing the FlareSolverr/Byparr approach.
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

const goBrowserTimeout = 65 * time.Second

type goBrowserRenderRequest struct {
	URL        string `json:"url"`
	TimeoutSec int    `json:"timeout_secs"`
}

type goBrowserRenderResponse struct {
	URL   string `json:"url"`
	HTML  string `json:"html"`
	Title string `json:"title"`
	Error string `json:"error,omitempty"`
}

// WithGoBrowserFallback enables fallback to a go-browser /render endpoint.
// The renderer returns fully JS-executed HTML via CloakBrowser with stealth
// fingerprinting, replacing the FlareSolverr/Byparr fallback.
func WithGoBrowserFallback(baseURL string) Option {
	return func(f *Fetcher) {
		if baseURL != "" {
			f.goBrowserURL = baseURL
		}
	}
}

// fetchViaGoBrowser calls the go-browser /render API and returns page HTML as bytes.
func (f *Fetcher) fetchViaGoBrowser(ctx context.Context, pageURL string) ([]byte, error) {
	body, err := json.Marshal(goBrowserRenderRequest{
		URL:        pageURL,
		TimeoutSec: 60,
	})
	if err != nil {
		return nil, fmt.Errorf("go-browser marshal: %w", err)
	}

	endpoint := f.goBrowserURL + "/render"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("go-browser request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: goBrowserTimeout}
	resp, err := client.Do(req) //nolint:gosec // URL is from internal config
	if err != nil {
		return nil, fmt.Errorf("go-browser call: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("go-browser read: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("go-browser HTTP %d: %s", resp.StatusCode, truncate(string(respBody)))
	}

	var result goBrowserRenderResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("go-browser parse: %w", err)
	}

	if result.Error != "" {
		return nil, fmt.Errorf("go-browser error: %s", result.Error)
	}
	if result.HTML == "" {
		return nil, errors.New("go-browser: empty response")
	}

	slog.Debug("go-browser fallback ok",
		slog.String("url", pageURL),
		slog.String("title", result.Title),
		slog.Int("size", len(result.HTML)))

	return []byte(result.HTML), nil
}
