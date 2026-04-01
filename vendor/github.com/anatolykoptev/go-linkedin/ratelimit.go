package linkedin

import (
	"math/rand/v2"
	"sync"
	"time"
)

// RateLimiter is a simple token bucket rate limiter.
type RateLimiter struct {
	mu        sync.Mutex
	max       int
	remaining int
	window    time.Duration
	resetAt   time.Time
}

// NewRateLimiter creates a RateLimiter that allows max requests per window duration.
func NewRateLimiter(max int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		max:       max,
		remaining: max,
		window:    window,
		resetAt:   time.Now().Add(window),
	}
}

// Allow returns true if a request is permitted within the current window.
func (r *RateLimiter) Allow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Now().After(r.resetAt) {
		r.remaining = r.max
		r.resetAt = time.Now().Add(r.window)
	}
	if r.remaining <= 0 {
		return false
	}
	r.remaining--
	return true
}

// Remaining returns the number of requests left in the current window.
func (r *RateLimiter) Remaining() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Now().After(r.resetAt) {
		return r.max
	}
	return r.remaining
}

// jitterDuration returns a random duration in [min, max).
func jitterDuration(min, max time.Duration) time.Duration {
	if max <= min {
		return min
	}
	return min + time.Duration(rand.Int64N(int64(max-min)))
}
