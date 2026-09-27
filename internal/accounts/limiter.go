package accounts

import (
	"context"
	"sync"
	"time"
)

// window tracks a fixed-window counter for one rate-limit key.
type window struct {
	count   int
	resetAt time.Time
}

// LoginLimiter is an in-process fixed-window rate limiter satisfying
// auth.RateLimiter (Allow(ctx, key, limit, window)). P0-local implementation:
// the admin listener is a single process on one host, so process-local windows
// are the correct blast radius; the interface is a deliberate seam —
// auth.RateLimiter's signature matches identity.RateLimiter verbatim, so a
// Redis-backed limiter can replace this without touching the call site.
//
// The zero value is ready to use: Allow lazily initialises hits/now under the
// mutex, so a bare LoginLimiter{} never nil-derefs.
//
// Fail-closed per the auth contract: the only error source is ctx.Err() —
// every other path returns a definitive allow/deny, never silently allows.
type LoginLimiter struct {
	mu   sync.Mutex
	hits map[string]window
	now  func() time.Time // test seam; nil → time.Now
}

// NewLoginLimiter returns a zero-value-ready limiter.
func NewLoginLimiter() *LoginLimiter {
	return &LoginLimiter{hits: make(map[string]window), now: time.Now}
}

// Allow implements auth.RateLimiter. Fixed window: the first hit inside a
// window resets the counter; hits beyond limit are denied until resetAt.
func (l *LoginLimiter) Allow(ctx context.Context, key string, limit int, win time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	// Lazy init keeps the zero value usable — NewLoginLimiter is the
	// conventional constructor but a bare LoginLimiter{} must not panic.
	if l.hits == nil {
		l.hits = make(map[string]window)
	}
	nowFn := l.now
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn()
	// Amortized cleanup: sweep expired windows once the map is large enough to
	// matter. Bounds memory on a long-lived process under many distinct IPs.
	if len(l.hits) > 4096 {
		for k, w := range l.hits {
			if now.After(w.resetAt) {
				delete(l.hits, k)
			}
		}
	}
	w := l.hits[key]
	if now.After(w.resetAt) || w.resetAt.IsZero() {
		w = window{resetAt: now.Add(win)}
	}
	w.count++
	l.hits[key] = w
	return w.count <= limit, nil
}
