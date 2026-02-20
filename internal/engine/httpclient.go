package engine

import (
	"fmt"
	"io"

	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// BrowserClient wraps tls-client with Chrome TLS fingerprint.
// Requests appear as Chrome 131+ to TLS fingerprinting (JA3 hash).
type BrowserClient struct {
	client tls_client.HttpClient
}

// NewBrowserClient creates a client that impersonates Chrome 131.
func NewBrowserClient() (*BrowserClient, error) {
	jar := tls_client.NewCookieJar()
	opts := []tls_client.HttpClientOption{
		tls_client.WithTimeoutSeconds(15),
		tls_client.WithClientProfile(profiles.Chrome_131),
		tls_client.WithNotFollowRedirects(),
		tls_client.WithCookieJar(jar),
		tls_client.WithInsecureSkipVerify(),
	}
	client, err := tls_client.NewHttpClient(nil, opts...)
	if err != nil {
		return nil, fmt.Errorf("tls-client init: %w", err)
	}
	return &BrowserClient{client: client}, nil
}

// Do executes a request with Chrome TLS fingerprint.
// Returns body bytes, HTTP status code, and any error.
func (bc *BrowserClient) Do(method, url string, headers map[string]string, body io.Reader) ([]byte, int, error) {
	req, err := fhttp.NewRequest(method, url, body)
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	// Chrome-like header order matters for fingerprinting
	req.Header[fhttp.HeaderOrderKey] = []string{
		"accept",
		"accept-language",
		"accept-encoding",
		"referer",
		"cookie",
		"user-agent",
	}

	resp, err := bc.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("tls request: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read body: %w", err)
	}

	return data, resp.StatusCode, nil
}

// chromeHeaders returns common Chrome browser headers.
func ChromeHeaders() map[string]string {
	return map[string]string{
		"accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
		"accept-language": "en-US,en;q=0.9",
		"accept-encoding": "gzip, deflate, br",
		"user-agent":      RandomUserAgent(),
	}
}
