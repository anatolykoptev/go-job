package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OxFetchResponse is the JSON response shape from ox-browser's POST /fetch endpoint.
// Spec reference: crates/js/src/fetch.rs in the ox-browser project.
type OxFetchResponse struct {
	Body       string `json:"body"`
	Status     int    `json:"status"`
	CFDetected bool   `json:"cf_detected"`
	CFType     string `json:"cf_type"`
	Error      string `json:"error,omitempty"`
}

// oxRequest is the JSON body sent to ox-browser's POST /fetch endpoint.
type oxRequest struct {
	URL         string `json:"url"`
	Method      string `json:"method,omitempty"`
	Body        string `json:"body,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

// OxFetch wraps ox-browser's POST /fetch endpoint. Chrome TLS impersonation
// bypasses anti-bot blocking of datacenter IPs (same failure Craigslist had in #346).
//
// When Cfg.OxBrowserURL is empty, returns an error — callers should fall back
// to Cfg.HTTPClient.Do or skip the source entirely.
//
// Per #348: targets /fetch (NOT /fetch-smart, which is DEPRECATED per ox-browser
// source and broken on search-results pages per ox-browser#110).
func OxFetch(ctx context.Context, method, targetURL string, body []byte, contentType string) (*OxFetchResponse, error) {
	if Cfg.OxBrowserURL == "" {
		return nil, fmt.Errorf("OxFetch: OxBrowserURL is empty (ox-browser disabled)")
	}
	if method == "" {
		method = http.MethodGet
	}

	reqBody, err := json.Marshal(oxRequest{
		URL:         targetURL,
		Method:      method,
		Body:        string(body),
		ContentType: contentType,
	})
	if err != nil {
		return nil, fmt.Errorf("OxFetch: marshal request: %w", err)
	}

	oxURL := Cfg.OxBrowserURL + "/fetch"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, oxURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("OxFetch: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OxFetch: call ox-browser: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("OxFetch: ox-browser returned %d: %s", resp.StatusCode, string(errBody))
	}

	var oxResp OxFetchResponse
	if err := json.NewDecoder(resp.Body).Decode(&oxResp); err != nil {
		return nil, fmt.Errorf("OxFetch: decode response: %w", err)
	}
	if oxResp.Error != "" {
		return &oxResp, fmt.Errorf("OxFetch: ox-browser error: %s", oxResp.Error)
	}
	return &oxResp, nil
}

// OxFetchGet is a convenience wrapper for GET requests.
func OxFetchGet(ctx context.Context, targetURL string) (*OxFetchResponse, error) {
	return OxFetch(ctx, http.MethodGet, targetURL, nil, "")
}

// OxFetchPost is a convenience wrapper for POST requests.
func OxFetchPost(ctx context.Context, targetURL string, body []byte, contentType string) (*OxFetchResponse, error) {
	return OxFetch(ctx, http.MethodPost, targetURL, body, contentType)
}
