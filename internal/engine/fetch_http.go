package engine

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
)

// fetchWithRetry performs an HTTP GET with retry logic using standard HTTP client.
// Prefer fetchBody() which routes through BrowserClient (proxy) when available.
func fetchWithRetry(ctx context.Context, fetchURL string, isHTML bool) (*http.Response, error) {
	return RetryHTTP(ctx, DefaultRetryConfig, func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
		if err != nil {
			return nil, err
		}

		req.Header.Set("User-Agent", RandomUserAgent())

		if isHTML {
			req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
			req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		} else {
			req.Header.Set("Accept", "text/plain,*/*;q=0.9")
		}

		req.Header.Set("Accept-Encoding", "gzip, deflate")

		return cfg.HTTPClient.Do(req)
	})
}

// fetchBody fetches URL body bytes, routing through BrowserClient (residential proxy)
// when available. Falls back to standard HTTP client if BrowserClient is nil.
func fetchBody(ctx context.Context, fetchURL string) ([]byte, error) {
	if cfg.BrowserClient != nil {
		headers := ChromeHeaders()
		headers["accept"] = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"

		return RetryDo(ctx, DefaultRetryConfig, func() ([]byte, error) {
			data, _, status, err := cfg.BrowserClient.Do("GET", fetchURL, headers, nil)
			if err != nil {
				return nil, err
			}
			if status != http.StatusOK {
				return nil, fmt.Errorf("fetch status %d for %s", status, fetchURL)
			}
			return data, nil
		})
	}

	resp, err := fetchWithRetry(ctx, fetchURL, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return readResponseBody(resp)
}

// readResponseBody reads the response body, handling gzip decompression if needed.
func readResponseBody(resp *http.Response) ([]byte, error) {
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		return io.ReadAll(gz)
	}
	return io.ReadAll(resp.Body)
}
