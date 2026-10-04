package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// ErrStealthBlocked indicates the target site actively challenged or blocked the request (e.g. Cloudflare / 403 / 429).
var ErrStealthBlocked = errors.New("stealth: request blocked or challenged by anti-bot")

// StealthFetch executes Tier-1 Chrome-TLS stealth GET.
// Returns (status, body, err) so the caller can distinguish blocks from network errors.
var StealthFetch = func(ctx context.Context, targetURL string, headers map[string]string) (status int, body []byte, err error) {
	if engine.Cfg.BrowserClient == nil {
		return 0, nil, errors.New("stealth: browser client not configured")
	}
	body, _, status, err = engine.Cfg.BrowserClient.DoCtx(ctx, "GET", targetURL, headers, nil)
	return status, body, err
}

// OxBrowserFetch executes Tier-2 ox-browser POST /fetch with Chrome TLS/JA3 impersonation and solver fallback.
var OxBrowserFetch = func(ctx context.Context, targetURL string, headers map[string]string) (status int, body []byte, err error) {
	if engine.Cfg.OxBrowserURL == "" {
		return 0, nil, errors.New("stealth: ox-browser URL not configured")
	}
	fetchURL := strings.TrimRight(engine.Cfg.OxBrowserURL, "/") + "/fetch"
	payload, err := json.Marshal(map[string]any{
		"url":     targetURL,
		"headers": headers,
		"timeout": int(engine.Cfg.FetchTimeout.Seconds()),
	})
	if err != nil {
		return 0, nil, fmt.Errorf("stealth ox-browser /fetch marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fetchURL, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, fmt.Errorf("stealth ox-browser /fetch request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if engine.Cfg.HTTPClient == nil {
		return 0, nil, errors.New("stealth: ox-browser HTTPClient not configured")
	}

	resp, err := engine.Cfg.HTTPClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("stealth ox-browser /fetch: %w", err)
	}
	defer resp.Body.Close()

	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return 0, nil, fmt.Errorf("stealth ox-browser /fetch body: %w", readErr)
	}

	var oxResp oxFetchResponse
	if jsonErr := json.Unmarshal(respBody, &oxResp); jsonErr != nil {
		return 0, nil, fmt.Errorf("stealth ox-browser /fetch decode: %w", jsonErr)
	}

	if resp.StatusCode == http.StatusOK {
		if oxResp.CfDetected || oxResp.Status == http.StatusForbidden || oxResp.Status == http.StatusTooManyRequests {
			return 0, nil, ErrStealthBlocked
		}
		if oxResp.Status == http.StatusOK && oxResp.Body != "" {
			return http.StatusOK, []byte(oxResp.Body), nil
		}
		return 0, nil, fmt.Errorf("stealth ox-browser /fetch: inner status %d", oxResp.Status)
	}

	if isOxBrowserCascadeError(oxResp.Error) {
		return 0, nil, ErrStealthBlocked
	}
	return 0, nil, fmt.Errorf("stealth ox-browser /fetch: wrapper %d: %s", resp.StatusCode, oxResp.Error)
}

// FetchHTMLWithLadder implements the standard two-tier stealth escalation ladder:
// Tier 1: Chrome-TLS direct GET -> Tier 2: ox-browser POST /fetch
func FetchHTMLWithLadder(ctx context.Context, targetURL string, headers map[string]string) ([]byte, error) {
	status, body, err := StealthFetch(ctx, targetURL, headers)
	if err == nil && status == http.StatusOK && len(body) > 0 {
		return body, nil
	}

	// Escalate to Tier 2 if blocked or failed
	oxStatus, oxBody, oxErr := OxBrowserFetch(ctx, targetURL, headers)
	if oxErr == nil && oxStatus == http.StatusOK && len(oxBody) > 0 {
		return oxBody, nil
	}

	if oxErr != nil {
		return nil, oxErr
	}
	return nil, fmt.Errorf("fetch failed with status %d", oxStatus)
}
