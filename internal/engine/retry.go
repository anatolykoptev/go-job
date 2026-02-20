package engine

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net"
	"net/http"
	"time"
)

// RetryConfig controls retry behavior.
type RetryConfig struct {
	MaxRetries  int
	InitialWait time.Duration
	MaxWait     time.Duration
	Multiplier  float64
}

// DefaultRetryConfig is suitable for most HTTP calls.
var DefaultRetryConfig = RetryConfig{
	MaxRetries:  3,
	InitialWait: 500 * time.Millisecond,
	MaxWait:     10 * time.Second,
	Multiplier:  2.0,
}

// RetryDo retries fn up to MaxRetries times with exponential backoff.
// Retries only on retryable errors; returns immediately on non-retryable or context cancellation.
func RetryDo[T any](ctx context.Context, rc RetryConfig, fn func() (T, error)) (T, error) {
	var zero T
	var lastErr error

	for attempt := 0; attempt <= rc.MaxRetries; attempt++ {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}

		result, err := fn()
		if err == nil {
			return result, nil
		}
		lastErr = err

		if !isRetryable(err) {
			return zero, err
		}

		if attempt < rc.MaxRetries {
			wait := time.Duration(float64(rc.InitialWait) * math.Pow(rc.Multiplier, float64(attempt)))
			if wait > rc.MaxWait {
				wait = rc.MaxWait
			}
			slog.Debug("retrying", slog.Int("attempt", attempt+1), slog.Duration("wait", wait), slog.Any("error", err))
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return zero, ctx.Err()
			}
		}
	}
	return zero, lastErr
}

// RetryHTTP executes an HTTP request function with retry logic.
// The function should build and send the request; RetryHTTP handles response status checks.
func RetryHTTP(ctx context.Context, rc RetryConfig, fn func() (*http.Response, error)) (*http.Response, error) {
	return RetryDo(ctx, rc, func() (*http.Response, error) {
		resp, err := fn()
		if err != nil {
			return nil, err
		}
		if isRetryableStatus(resp.StatusCode) {
			resp.Body.Close()
			return nil, &httpStatusError{StatusCode: resp.StatusCode}
		}
		return resp, nil
	})
}

// httpStatusError wraps a retryable HTTP status code.
type httpStatusError struct {
	StatusCode int
}

func (e *httpStatusError) Error() string {
	return http.StatusText(e.StatusCode)
}

// isRetryable returns true for transient errors worth retrying.
func isRetryable(err error) bool {
	// HTTP status errors
	var httpErr *httpStatusError
	if errors.As(err, &httpErr) {
		return true // already filtered by isRetryableStatus
	}

	// Connection errors (dial failures, connection refused, etc.)
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}

	// DNS errors
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}

	// Timeout errors (net.Error includes OpError, so check after OpError)
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}

	return false
}

// isRetryableStatus returns true for HTTP status codes worth retrying.
func isRetryableStatus(code int) bool {
	switch code {
	case 429, 500, 502, 503, 504:
		return true
	}
	return false
}
