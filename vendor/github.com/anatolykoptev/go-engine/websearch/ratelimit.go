package websearch

import (
	"context"
	"fmt"

	"golang.org/x/time/rate"
)

// RateLimited wraps a Provider with a token bucket rate limiter.
type RateLimited struct {
	inner   Provider
	limiter *rate.Limiter
}

// NewRateLimited creates a rate-limited provider.
// rps: requests per second, burst: max burst size.
func NewRateLimited(p Provider, rps float64, burst int) *RateLimited {
	return &RateLimited{
		inner:   p,
		limiter: rate.NewLimiter(rate.Limit(rps), burst),
	}
}

// Search waits for a rate limit token, then delegates to the inner provider.
func (r *RateLimited) Search(ctx context.Context, query string, opts SearchOpts) ([]Result, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limit: %w", err)
	}
	return r.inner.Search(ctx, query, opts)
}
