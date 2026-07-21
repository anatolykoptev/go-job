// Package pgutil provides PostgreSQL connection helpers with retry and
// graceful degradation support. It wraps go-kit/retry with database-specific
// defaults and two connection modes:
//
//   - Required (default): retries and returns error if all attempts fail.
//   - Optional: retries and returns nil pool on failure (service starts in degraded mode).
package pgutil

import (
	"context"
	"log/slog"
	"time"

	"github.com/anatolykoptev/go-kit/retry"
)

// Default retry parameters for postgres startup connections.
const (
	DefaultMaxAttempts    = 10
	DefaultInitDelay      = time.Second
	DefaultMaxDelay       = 30 * time.Second
	DefaultHealthInterval = 15 * time.Second
)

// Options configures the Connect and Lazy behavior.
type Options struct {
	// MaxAttempts overrides the default retry count (10).
	MaxAttempts int
	// InitDelay overrides the initial backoff delay (1s).
	InitDelay time.Duration
	// MaxDelay overrides the maximum backoff delay (30s).
	MaxDelay time.Duration
	// Optional makes Connect return (nil, nil) instead of an error
	// when all retry attempts are exhausted. Use for features that can
	// operate without postgres (graceful degradation).
	Optional bool
	// Logger overrides the default slog.Default() logger.
	Logger *slog.Logger
	// Name is used in log messages (e.g. "postgres", "billing-db").
	Name string
	// HealthInterval sets the health check period for Lazy (default 15s).
	// Only used by Lazy, ignored by Connect.
	HealthInterval time.Duration
}

func (o *Options) applyDefaults() {
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = DefaultMaxAttempts
	}
	if o.InitDelay <= 0 {
		o.InitDelay = DefaultInitDelay
	}
	if o.MaxDelay <= 0 {
		o.MaxDelay = DefaultMaxDelay
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Name == "" {
		o.Name = "postgres"
	}
	if o.HealthInterval <= 0 {
		o.HealthInterval = DefaultHealthInterval
	}
}

// Connect retries a connection function with exponential backoff and jitter.
// The connectFn should create a connection/pool, ping it, and return it.
// On failure, connectFn should clean up (e.g. pool.Close()) before returning error.
//
// If opts.Optional is true, returns (nil, nil) when all attempts are exhausted
// instead of returning an error — enabling graceful degradation.
func Connect[T any](ctx context.Context, opts Options, connectFn func(ctx context.Context) (T, error)) (T, error) {
	opts.applyDefaults()

	logger := opts.Logger
	name := opts.Name

	result, err := retry.Do(ctx, retry.Options{
		MaxAttempts:  opts.MaxAttempts,
		InitialDelay: opts.InitDelay,
		MaxDelay:     opts.MaxDelay,
		Jitter:       true,
		OnRetry: func(attempt int, err error) {
			logger.Warn(name+" not ready, retrying",
				slog.Int("attempt", attempt),
				slog.Any("error", err))
		},
	}, func() (T, error) {
		return connectFn(ctx)
	})
	if err != nil {
		if opts.Optional {
			logger.Warn(name+" unavailable, running in degraded mode",
				slog.Any("error", err))
			var zero T
			return zero, nil
		}
		return result, err
	}

	logger.Info(name + " connected")
	return result, nil
}
