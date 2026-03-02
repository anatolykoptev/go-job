package engine

import (
	"context"
	"encoding/json"
	"time"

	"github.com/anatolykoptev/go-kit/cache"
)

// searchCache is the singleton cache instance.
var searchCache *cache.Cache

// CacheTTL controls how long results stay cached.
var CacheTTL = 15 * time.Minute

// JobDetailsTTL controls how long job details stay cached (descriptions rarely change).
var JobDetailsTTL = 24 * time.Hour

// InitCache sets up the 2-tier cache. Call after Init().
// redisURL can be empty to disable L2.
func InitCache(redisURL string, ttl time.Duration, maxEntries int, _ time.Duration) {
	CacheTTL = ttl
	searchCache = cache.New(cache.Config{
		RedisURL:      redisURL,
		Prefix:        "gj:",
		L1MaxItems:    maxEntries,
		L1TTL:         ttl,
		L2TTL:         ttl,
		JitterPercent: 0.1,
	})
}

// CacheKey builds a deterministic cache key from parts.
func CacheKey(parts ...string) string {
	return cache.Key(parts...)
}

// CacheGet tries L1, then L2. Returns the cached SmartSearchOutput and true on hit.
func CacheGet(ctx context.Context, key string) (SmartSearchOutput, bool) {
	if searchCache == nil {
		return SmartSearchOutput{}, false
	}
	data, ok := searchCache.Get(ctx, key)
	if !ok {
		return SmartSearchOutput{}, false
	}
	var out SmartSearchOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return SmartSearchOutput{}, false
	}
	return out, true
}

// CacheSet stores value in both L1 and L2.
func CacheSet(ctx context.Context, key string, value SmartSearchOutput) {
	if searchCache == nil {
		return
	}
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	searchCache.Set(ctx, key, data)
}

// CacheStats returns current cache hit/miss counters.
func CacheStats() (hits, misses int64) {
	if searchCache == nil {
		return 0, 0
	}
	s := searchCache.Stats()
	return s.L1Hits + s.L2Hits, s.L1Misses + s.L2Misses
}

// CacheGetJobDetails retrieves cached job details by URL.
func CacheGetJobDetails(ctx context.Context, jobURL string) (string, bool) {
	if searchCache == nil {
		return "", false
	}
	key := CacheKey("jd", jobURL)
	data, ok := searchCache.Get(ctx, key)
	if !ok {
		return "", false
	}
	return string(data), true
}

// CacheSetJobDetails stores job details by URL.
func CacheSetJobDetails(ctx context.Context, jobURL, details string) {
	if searchCache == nil {
		return
	}
	key := CacheKey("jd", jobURL)
	searchCache.SetWithTTL(ctx, key, []byte(details), JobDetailsTTL)
}

// CacheLoadJSON tries to load a cached value of type T from the engine cache.
// Returns the decoded value and true on hit; zero value and false on miss or decode error.
func CacheLoadJSON[T any](ctx context.Context, key string) (T, bool) {
	cached, ok := CacheGet(ctx, key)
	if !ok {
		var zero T
		return zero, false
	}
	var out T
	if err := json.Unmarshal([]byte(cached.Answer), &out); err != nil {
		var zero T
		return zero, false
	}
	return out, true
}

// CacheStoreJSON marshals v and stores it in the engine cache.
func CacheStoreJSON[T any](ctx context.Context, key, query string, v T) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	CacheSet(ctx, key, SmartSearchOutput{
		Query:  query,
		Answer: string(data),
	})
}
