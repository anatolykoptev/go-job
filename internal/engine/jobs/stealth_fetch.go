package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// ErrStealthBlocked indicates that an upstream consumer HTML site actively
// refused or challenged the request (e.g. HTTP 403/429/503 or Cloudflare challenge).
var ErrStealthBlocked = errors.New("upstream anti-bot block detected")

type oxBrowserFetchReq struct {
	URL string `json:"url"`
}

type oxBrowserFetchResp struct {
	Status     int    `json:"status"`
	Body       string `json:"body"`
	CFDetected bool   `json:"cf_detected"`
	CFType     string `json:"cf_type,omitempty"`
	Error      string `json:"error,omitempty"`
}

// FetchStealthHTML fetches raw HTML from a consumer job site using the two-tier
// anti-bot ladder (#348):
//  1. Primary: stealth Chrome-TLS via engine.Cfg.BrowserClient (when configured).
//  2. Escalation: ox-browser POST /fetch when the primary tier is refused,
//     challenged, or unavailable and engine.Cfg.OxBrowserURL is configured.
//
// Preserves the distinction between anti-bot blocks (ErrStealthBlocked) and
// transport errors so per-source outcome metrics remain accurate.
func FetchStealthHTML(ctx context.Context, targetURL string) ([]byte, error) {
	var primaryErr error

	if engine.Cfg.BrowserClient != nil {
		body, status, err := engine.Cfg.BrowserClient.GetRaw(ctx, targetURL)
		if err == nil && status == http.StatusOK && len(body) > 0 {
			return body, nil
		}
		if err != nil {
			primaryErr = err
		} else if status == http.StatusForbidden || status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
			primaryErr = fmt.Errorf("%w: status %d", ErrStealthBlocked, status)
		} else {
			primaryErr = fmt.Errorf("unexpected status %d", status)
		}
	}

	if engine.Cfg.OxBrowserURL != "" {
		body, err := fetchViaOxBrowser(ctx, targetURL)
		if err == nil {
			return body, nil
		}
		return nil, err
	}

	if primaryErr != nil {
		return nil, primaryErr
	}
	return engine.FetchProxyBody(ctx, targetURL)
}

func fetchViaOxBrowser(ctx context.Context, targetURL string) ([]byte, error) {
	payload, err := json.Marshal(oxBrowserFetchReq{URL: targetURL})
	if err != nil {
		return nil, err
	}
	 endpoint := engine.Cfg.OxBrowserURL + "/fetch"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := engine.Cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req) //nolint:gosec // intentional outbound request to configured ox-browser service
	if err != nil {
		return nil, fmt.Errorf("ox-browser POST /fetch: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("ox-browser read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ox-browser HTTP %d", resp.StatusCode)
	}

	var parsed oxBrowserFetchResp
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("ox-browser decode: %w", err)
	}
	if parsed.Error != "" {
		return nil, fmt.Errorf("ox-browser upstream error: %s", parsed.Error)
	}
	if parsed.CFDetected || parsed.Status == http.StatusForbidden || parsed.Status == http.StatusTooManyRequests || parsed.Status == http.StatusServiceUnavailable {
		return nil, fmt.Errorf("%w: status=%d cf_type=%s", ErrStealthBlocked, parsed.Status, parsed.CFType)
	}
	if parsed.Status != 0 && parsed.Status != http.StatusOK {
		return nil, fmt.Errorf("ox-browser upstream status %d", parsed.Status)
	}
	return []byte(parsed.Body), nil
}
