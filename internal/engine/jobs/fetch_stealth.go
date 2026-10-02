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

// ErrBlocked is returned when any tier detected a genuine anti-bot block.
var ErrBlocked = errors.New("blocked: anti-bot refusal detected")

// FetchHTMLWithStealth is a helper function that tries stealth Chrome-TLS first,
// and uses ox-browser POST /fetch on refusal.
func FetchHTMLWithStealth(ctx context.Context, method, pageURL string, headers map[string]string, reqBody []byte) (status int, body []byte, err error) {
	if headers == nil {
		headers = engine.ChromeHeaders()
		headers["accept"] = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
	}

	sStatus, sBody, sErr := StealthFetchFunc(ctx, method, pageURL, headers, reqBody)
	if sErr == nil && sStatus == http.StatusOK && len(sBody) > 0 {
		return sStatus, sBody, nil
	}

	stealthRefused := sErr == nil && isRefusalStatus(sStatus)
	if stealthRefused {
		slog.Warn("fetch: stealth tier blocked, escalating to ox-browser",
			slog.String("url", pageURL),
			slog.Int("status", sStatus))
	} else {
		slog.Warn("fetch: stealth tier transport error, escalating",
			slog.String("url", pageURL),
			slog.Int("status", sStatus),
			slog.Any("error", sErr))
	}

	if engine.Cfg.OxBrowserURL == "" {
		slog.Warn("fetch: ox-browser /fetch tier skipped (OxBrowserURL empty)",
			slog.String("url", pageURL))
		if sErr != nil {
			return 0, nil, sErr
		}
		if stealthRefused {
			return 0, nil, ErrBlocked
		}
		return sStatus, nil, fmt.Errorf("fetch stealth status: %d", sStatus)
	}

	oxStatus, oxBody, oxErr := OxBrowserFetchFunc(ctx, method, pageURL, headers, reqBody)
	if oxErr == nil && oxStatus == http.StatusOK && len(oxBody) > 0 {
		return oxStatus, oxBody, nil
	}

	oxRefused := oxErr == nil && isRefusalStatus(oxStatus) || errors.Is(oxErr, ErrBlocked)

	if stealthRefused && oxRefused {
		return 0, nil, errors.Join(
			ErrBlocked,
			fmt.Errorf("fetch: stealth status=%d ox-browser status=%d", sStatus, oxStatus),
		)
	}

	var tierErrs []error
	if sErr != nil {
		tierErrs = append(tierErrs, fmt.Errorf("fetch stealth: %w", sErr))
	} else if sStatus != http.StatusOK {
		tierErrs = append(tierErrs, fmt.Errorf("fetch stealth status: %d", sStatus))
	}

	if oxErr != nil {
		tierErrs = append(tierErrs, fmt.Errorf("fetch ox-browser: %w", oxErr))
	} else if oxStatus != http.StatusOK {
		tierErrs = append(tierErrs, fmt.Errorf("fetch ox-browser status: %d", oxStatus))
	}

	return 0, nil, errors.Join(tierErrs...)
}

var StealthFetchFunc = stealthFetch

func stealthFetch(ctx context.Context, method, pageURL string, headers map[string]string, reqBody []byte) (status int, body []byte, err error) {
	if engine.Cfg.BrowserClient == nil {
		return 0, nil, errors.New("stealth client not configured")
	}
	var r io.Reader
	if reqBody != nil {
		r = bytes.NewReader(reqBody)
	}
	body, _, status, err = engine.Cfg.BrowserClient.DoCtx(ctx, method, pageURL, headers, r)
	return status, body, err
}

var OxBrowserFetchFunc = oxBrowserFetch

func oxBrowserFetch(ctx context.Context, method, pageURL string, headers map[string]string, reqBody []byte) (status int, body []byte, err error) {
	fetchURL := strings.TrimRight(engine.Cfg.OxBrowserURL, "/") + "/fetch"

	reqMap := map[string]any{
		"url":     pageURL,
		"method":  method,
		"headers": headers,
		"timeout": int(engine.Cfg.FetchTimeout.Seconds()),
	}
	if reqBody != nil {
		reqMap["body"] = string(reqBody)
	}

	payload, err := json.Marshal(reqMap)
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
		return 0, nil, fmt.Errorf("ox-browser /fetch: %w", err)
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return 0, nil, fmt.Errorf("ox-browser /fetch body: %w", readErr)
	}

	var oxResp oxFetchResponse
	if jsonErr := json.Unmarshal(respBody, &oxResp); jsonErr != nil {
		return 0, nil, fmt.Errorf("ox-browser /fetch decode: %w", jsonErr)
	}

	if resp.StatusCode == http.StatusOK {
		if oxResp.CfDetected {
			return 0, nil, ErrBlocked
		}
		if oxResp.Status == http.StatusForbidden || oxResp.Status == http.StatusTooManyRequests {
			return 0, nil, ErrBlocked
		}
		if oxResp.Status == http.StatusOK && oxResp.Body != "" {
			return http.StatusOK, []byte(oxResp.Body), nil
		}
		return 0, nil, fmt.Errorf("ox-browser /fetch: inner status %d", oxResp.Status)
	}

	if isOxBrowserCascadeError(oxResp.Error) {
		return 0, nil, ErrBlocked
	}
	return 0, nil, fmt.Errorf("ox-browser /fetch: wrapper %d: %s", resp.StatusCode, oxResp.Error)
}

// oxFetchResponse is the JSON body of ox-browser's POST /fetch response.
type oxFetchResponse struct {
	Status     int               `json:"status"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
	CfDetected bool              `json:"cf_detected"`
	CfType     string            `json:"cf_type,omitempty"`
	ElapsedMs  int64             `json:"elapsed_ms"`
	Error      string            `json:"error,omitempty"`
}

// isOxBrowserCascadeError returns true if the ox-browser error string names an
// exhausted anti-bot cascade (solver failure or per-domain solver cooldown).
func isOxBrowserCascadeError(oxErr string) bool {
	return strings.Contains(oxErr, "solver") || strings.Contains(oxErr, "cf_clearance")
}

// isRefusalStatus returns true for HTTP status codes that represent a genuine
// anti-bot refusal (as opposed to a transport error or a successful response).
func isRefusalStatus(status int) bool {
	return status == http.StatusForbidden || status == http.StatusTooManyRequests
}
