package cache

import "time"

// Config configures the cache.
type Config struct {
	// RedisURL is the Redis connection URL. Empty means L1-only mode.
	RedisURL string

	// RedisDB selects the Redis database number (default 0).
	RedisDB int

	// Prefix is prepended to all Redis keys (e.g. "gs:" for go-search).
	Prefix string

	// L1MaxItems is the max number of items in memory (default 1000).
	L1MaxItems int

	// L1TTL is the TTL for L1 cache entries (default 30m).
	L1TTL time.Duration

	// L2TTL is the TTL for L2 Redis entries (default 24h). Ignored if no Redis.
	L2TTL time.Duration

	// JitterPercent adds random TTL variation to prevent cache stampedes.
	// 0.1 means ±10% jitter. 0 disables jitter (default).
	JitterPercent float64

	// L2 is an optional L2 store (e.g. Redis). If set, overrides RedisURL.
	// Pass a mock here in tests instead of using a real Redis.
	L2 L2

	// OnEvict is called after an entry is removed from L1.
	// Called outside the cache lock — safe to call cache methods.
	// Must be goroutine-safe (may fire from multiple goroutines concurrently).
	OnEvict func(key string, data []byte, reason EvictReason)

	// MaxWeight is the maximum total weight of L1 entries in bytes.
	// 0 (default) disables weight-based eviction entirely.
	// Requires Weigher to be set; if Weigher is nil, MaxWeight is ignored.
	MaxWeight int64

	// Weigher computes the weight (e.g. byte size) of a cache entry.
	// When nil (default), no weight tracking is performed and MaxWeight is ignored.
	// The zero-value nil preserves exact existing behavior for all callers.
	Weigher func(key string, data []byte) int64

	// IdleTTL evicts an entry when it has not been accessed for this duration.
	// 0 (default) disables time-to-idle eviction entirely — no goroutine is spawned,
	// no lastAccess updates are checked. Exact existing behavior is preserved.
	IdleTTL time.Duration
}

func (c *Config) applyDefaults() {
	if c.L1MaxItems <= 0 {
		c.L1MaxItems = 1000
	}
	if c.L1TTL <= 0 {
		c.L1TTL = 30 * time.Minute
	}
	if c.L2TTL <= 0 {
		c.L2TTL = 24 * time.Hour
	}
}
