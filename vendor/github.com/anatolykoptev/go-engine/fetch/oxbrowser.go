// Package fetch — ox-browser fallback fetcher.
//
// When proxy fails, fetches the raw page via ox-browser's /fetch endpoint.
// Uses wreq+BoringSSL for TLS fingerprint bypass, with optional headless solve
// for Cloudflare challenges. Faster than Byparr for non-JS pages.
//
// The returned bytes are the raw page body (HTML) — callers feed them into an
// HTML extractor. Do NOT switch this to /read with format:"llm": that returns
// markdown, which an HTML extractor silently mis-parses as empty/garbage with
// a nil error (silent corruption). The /read llm-format path is consumed
// separately by go-search's oxReadability branch (go-search#232).
//
// Auth: requests to the ox-browser origin carry X-Internal-Secret from the
// INTERNAL_SERVICE_SECRET env var when it is set (go-kit svcauth). The header
// is scoped to the ox-browser origin and stripped from redirects to any other
// origin. Env unset → no header, behaviour unchanged (ox-browser#173).
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

	"github.com/anatolykoptev/go-kit/svcauth"
)

const oxBrowserTimeout = 30 * time.Second

// oxFetchRequest is the /fetch request body. /fetch accepts {url, headers,
// timeout}; headers is omitted so ox-browser uses its own stealth headers
// (overriding them would defeat the TLS-fingerprint bypass). timeout is a real
// field on /fetch (unlike /read, which has no timeout parameter).
type oxFetchRequest struct {
	URL     string `json:"url"`
	Timeout int    `json:"timeout"`
}

// oxFetchResponse is the /fetch response. /fetch returns {status, headers,
// body, cf_detected, cf_type, elapsed_ms, error} where body is the raw page.
// Method (present on the deprecated /fetch-smart) is absent on /fetch and is
// not logged.
type oxFetchResponse struct {
	Status    int    `json:"status"`
	Body      string `json:"body"`
	CFDetect  bool   `json:"cf_detected"`
	CFType    string `json:"cf_type,omitempty"`
	ElapsedMs int    `json:"elapsed_ms"`
	Error     string `json:"error,omitempty"`
}

// WithOxBrowser enables fallback to an ox-browser /fetch endpoint.
// Requests to baseURL carry X-Internal-Secret from INTERNAL_SERVICE_SECRET
// when set (scoped to that origin only, redirect-safe).
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

	endpoint := f.oxBrowserURL + "/fetch"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ox-browser request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// svcauth reads INTERNAL_SERVICE_SECRET per call and routes
	// X-Internal-Secret only to the ox-browser origin (stripped elsewhere,
	// redirect-safe). nil base = http.DefaultTransport, preserving today's
	// implicit transport; empty env → no header.
	transport, err := svcauth.FromEnv(nil, f.oxBrowserURL)
	if err != nil {
		return nil, fmt.Errorf("ox-browser svcauth: %w", err)
	}
	client := &http.Client{Timeout: oxBrowserTimeout + 5*time.Second, Transport: transport}
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
		// /fetch returns non-200 with a structured `error` field when the
		// upstream fetch fails (e.g. 502). Surface that distinctly from a
		// transport failure: a transport failure returns before we ever get
		// an HTTP status (the client.Do error path above), so reaching here
		// with a 502 + useful error must not read as a generic HTTP error.
		var errResp oxFetchResponse
		if jsonErr := json.Unmarshal(respBody, &errResp); jsonErr == nil && errResp.Error != "" {
			return nil, fmt.Errorf("ox-browser HTTP %d: %s", resp.StatusCode, errResp.Error)
		}
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
		slog.Bool("cf", result.CFDetect),
		slog.String("cf_type", result.CFType),
		slog.Int("elapsed_ms", result.ElapsedMs))

	return []byte(result.Body), nil
}
