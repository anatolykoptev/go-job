package engine

import (
	"context"
	"net/http"

	stealth "github.com/anatolykoptev/go-stealth"
)

// Re-export stealth types and functions for engine consumers.
type BrowserClient = stealth.BrowserClient

var DefaultRetryConfig = stealth.DefaultRetryConfig

func ChromeHeaders() map[string]string { return stealth.ChromeHeaders() }
func RandomUserAgent() string          { return stealth.RandomUserAgent() }
func IsRetryableStatus(code int) bool  { return stealth.IsRetryableStatus(code) }

func RetryDo[T any](ctx context.Context, rc stealth.RetryConfig, fn func() (T, error)) (T, error) {
	return stealth.RetryDo(ctx, rc, fn)
}

func RetryHTTP(ctx context.Context, rc stealth.RetryConfig, fn func() (*http.Response, error)) (*http.Response, error) {
	return stealth.RetryHTTP(ctx, rc, fn)
}
