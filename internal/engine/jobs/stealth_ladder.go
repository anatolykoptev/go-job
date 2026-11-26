package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// ErrConnectorBlocked indicates that an anti-bot challenge (403/429/cf_detected)
// refused the request across both stealth and ox-browser tiers.
var ErrConnectorBlocked = errors.New("stealth: blocked by anti-bot challenge")

// StealthFetchSeam is the Tier-1 transport function (overridable in tests).
var StealthFetchSeam = func(ctx context.Context, method, targetURL string, headers map[string]string, reqBody []byte) (status int, body []byte, err error) {
	if engine.Cfg.BrowserClient == nil {
		return 0, nil, errors.New("stealth client not configured")
	}
	var r io.Reader
	if len(reqBody) > 0 {
		r = bytes.NewReader(reqBody)
	}
	body, _, status, err = engine.Cfg.BrowserClient.DoCtx(ctx, method, targetURL, headers, r)
	return status, body, err
}

// OxFetchSeam is the Tier-2 transport function invoking ox-browser POST /fetch (overridable in tests).
var OxFetchSeam = func(ctx context.Context, method, targetURL string, headers map[string]string, reqBody []byte) (status int, body []byte, err error) {
	if engine.Cfg.OxBrowserURL == "" {
		return 0, nil, errors.New("ox-browser URL not configured")
	}
	fetchURL := strings.TrimRight(engine.Cfg.OxBrowserURL, "/") + "/fetch"
	timeoutSec := 30
	if engine.Cfg.FetchTimeout > 0 {
		timeoutSec = int(engine.Cfg.FetchTimeout.Seconds())
	}
	payloadMap := map[string]any{
		"url":     targetURL,
		"headers": headers,
		"method":  method,
		"timeout": timeoutSec,
	}
	if len(reqBody) > 0 {
		payloadMap["body"] = string(reqBody)
	}
	payload, err := json.Marshal(payloadMap)
	if err != nil {
		return 0, nil, fmt.Errorf("ox-browser /fetch marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fetchURL, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, fmt.Errorf("ox-browser /fetch request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if engine.Cfg.HTTPClient == nil {
		return 0, nil, errors.New("ox-browser /fetch: HTTPClient not configured")
	}
	resp, err := engine.Cfg.HTTPClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("ox-browser /fetch Do: %w", err)
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return 0, nil, fmt.Errorf("ox-browser /fetch read: %w", readErr)
	}

	var oxResp oxFetchResponse
	if jsonErr := json.Unmarshal(respBody, &oxResp); jsonErr != nil {
		return 0, nil, fmt.Errorf("ox-browser /fetch unmarshal: %w", jsonErr)
	}

	if resp.StatusCode == http.StatusOK {
		if oxResp.CfDetected || oxResp.Status == http.StatusForbidden || oxResp.Status == http.StatusTooManyRequests {
			return 0, nil, ErrConnectorBlocked
		}
		if oxResp.Status == http.StatusOK && oxResp.Body != "" {
			return http.StatusOK, []byte(oxResp.Body), nil
		}
		if oxResp.Error != "" && isOxBrowserCascadeError(oxResp.Error) {
			return 0, nil, ErrConnectorBlocked
		}
		return oxResp.Status, []byte(oxResp.Body), nil
	}

	if resp.StatusCode == http.StatusBadGateway && isOxBrowserCascadeError(oxResp.Error) {
		return 0, nil, ErrConnectorBlocked
	}
	return 0, nil, fmt.Errorf("ox-browser returned wrapper status %d: %s", resp.StatusCode, oxResp.Error)
}

// StealthLadderFetch executes the standardized 2-tier transport ladder:
// Tier 1: Stealth Chrome-TLS client.
// Tier 2: Ox-browser POST /fetch on refusal (403, 429, or transport error).
// Returns (status, body, err) with ErrConnectorBlocked on anti-bot refusal.
func StealthLadderFetch(ctx context.Context, method, targetURL string, headers map[string]string, reqBody []byte) (status int, body []byte, err error) {
	if headers == nil {
		headers = make(map[string]string)
	}
	if headers["User-Agent"] == "" && headers["user-agent"] == "" {
		for k, v := range engine.ChromeHeaders() {
			if _, exists := headers[k]; !exists {
				headers[k] = v
			}
		}
	}

	// Tier 1: Stealth Chrome-TLS
	sStatus, sBody, sErr := StealthFetchSeam(ctx, method, targetURL, headers, reqBody)
	if sErr == nil && sStatus == http.StatusOK && len(sBody) > 0 {
		return sStatus, sBody, nil
	}

	// Escalate to Tier 2 on refusal or error
	slog.Warn("stealth ladder: tier 1 failed or refused, escalating to ox-browser /fetch",
		slog.String("url", targetURL),
		slog.Int("tier1_status", sStatus),
		slog.Any("tier1_error", sErr),
	)

	return OxFetchSeam(ctx, method, targetURL, headers, reqBody)
}
