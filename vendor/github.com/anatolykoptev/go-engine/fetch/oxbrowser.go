// Package fetch — ox-browser fallback fetcher.
//
// When proxy fails, fetches page content via ox-browser's /fetch-smart endpoint.
// Uses wreq+BoringSSL for TLS fingerprint bypass, with optional headless solve
// for Cloudflare challenges. Faster than Byparr for non-JS pages.
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

const oxBrowserTimeout = 30 * time.Second

type oxFetchRequest struct {
	URL     string `json:"url"`
	Timeout int    `json:"timeout"`
}

type oxFetchResponse struct {
	Status    int    `json:"status"`
	Body      string `json:"body"`
	Method    string `json:"method"`
	CFDetect  bool   `json:"cf_detected"`
	ElapsedMs int    `json:"elapsed_ms"`
	Error     string `json:"error,omitempty"`
}

// WithOxBrowser enables fallback to an ox-browser /fetch-smart endpoint.
func WithOxBrowser(baseURL string) Option {
	return func(f *Fetcher) {
		if baseURL != "" {
			f.oxBrowserURL = baseURL
		}
	}
}

func (f *Fetcher) fetchViaOxBrowser(ctx context.Context, pageURL string) ([]byte, error) {
	body, err := json.Marshal(oxFetchRequest{
		URL:     pageURL,
		Timeout: int(oxBrowserTimeout.Seconds()),
	})
	if err != nil {
		return nil, fmt.Errorf("ox-browser marshal: %w", err)
	}

	endpoint := f.oxBrowserURL + "/fetch-smart"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ox-browser request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: oxBrowserTimeout + 5*time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ox-browser call: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ox-browser read: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ox-browser HTTP %d: %s", resp.StatusCode, truncate(string(respBody)))
	}

	var result oxFetchResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("ox-browser parse: %w", err)
	}

	if result.Error != "" {
		return nil, fmt.Errorf("ox-browser error: %s", result.Error)
	}
	if result.Body == "" {
		return nil, errors.New("ox-browser: empty response")
	}

	slog.Debug("ox-browser fallback ok",
		slog.String("url", pageURL),
		slog.String("method", result.Method),
		slog.Bool("cf", result.CFDetect),
		slog.Int("elapsed_ms", result.ElapsedMs))

	return []byte(result.Body), nil
}
