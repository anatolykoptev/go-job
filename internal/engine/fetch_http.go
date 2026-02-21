package engine

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/cenkalti/backoff/v5"
)

// newFetchClient creates an HTTP client with proper settings for web scraping.
func newFetchClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        10,
			MaxIdleConnsPerHost: 5,
			IdleConnTimeout:     30 * time.Second,
			DisableCompression:  false,
			TLSHandshakeTimeout: 15 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			return nil
		},
	}
}

// fetchWithRetry performs an HTTP GET with retry logic using exponential backoff.
// isHTML controls Accept headers: HTML for web pages, text/plain for raw files.
func fetchWithRetry(ctx context.Context, fetchURL string, isHTML bool) (*http.Response, error) {
	client := newFetchClient()

	operation := func() (*http.Response, error) {
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

		resp, err := client.Do(req)
		if err != nil {
			return nil, backoff.Permanent(err)
		}

		if IsRetryableStatus(resp.StatusCode) {
			resp.Body.Close()
			return nil, fmt.Errorf("status %d", resp.StatusCode)
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, backoff.Permanent(fmt.Errorf("status %d", resp.StatusCode))
		}

		return resp, nil
	}

	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 1 * time.Second
	bo.MaxInterval = 10 * time.Second

	return backoff.Retry(ctx, operation, backoff.WithBackOff(bo), backoff.WithMaxTries(3), backoff.WithMaxElapsedTime(30*time.Second))
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
