package shell

import (
	"context"
	"sync"
	"time"
)

// CachedBadge wraps a Badge closure with a TTL cache so the function is called
// at most once per ttl window. This avoids a per-render DB query (the primary
// footgun when using raw Badge closures against a live COUNT).
//
// Usage:
//
//	Badge: shell.CachedBadge(30*time.Second, func(ctx context.Context) string {
//	    n := store.CountPending(ctx)
//	    if n == 0 { return "" }
//	    return strconv.Itoa(n)
//	})
//
// The cache is per-CachedBadge call (each call returns a distinct closure with
// its own state). Thread-safe.
func CachedBadge(ttl time.Duration, fn func(context.Context) string) func(context.Context) string {
	var (
		mu      sync.Mutex
		last    string
		expires time.Time
	)
	return func(ctx context.Context) string {
		mu.Lock()
		defer mu.Unlock()
		if time.Now().Before(expires) {
			return last
		}
		last = fn(ctx)
		expires = time.Now().Add(ttl)
		return last
	}
}
