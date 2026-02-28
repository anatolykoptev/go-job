package engine

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
)

// fetchWithRetry performs an HTTP GET with retry logic using go-stealth.
// isHTML controls Accept headers: HTML for web pages, text/plain for raw files.
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
