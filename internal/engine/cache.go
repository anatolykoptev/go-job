package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// Cache provides 2-tier caching: L1 in-memory + L2 Redis.
// L1 is fast but lost on restart. L2 survives restarts.
var searchCache *tieredCache

// CacheTTL controls how long results stay cached.
var CacheTTL = 15 * time.Minute

// Cache metrics — atomic counters for thread-safe access.
var (
	cacheHits   atomic.Int64
	cacheMisses atomic.Int64
)

// tieredCache implements L1 (memory) + L2 (Redis) caching.
type tieredCache struct {
	l1              sync.Map      // key → *cacheEntry
	rdb             *redis.Client // nil if Redis unavailable
	ttl             time.Duration
	maxEntries      int
	cleanupInterval time.Duration
}

type cacheEntry struct {
	data      []byte
	expiresAt time.Time
}

// InitCache sets up the 2-tier cache. Call after Init().
// redisURL can be empty to disable L2.
func InitCache(redisURL string, ttl time.Duration, maxEntries int, cleanupInterval time.Duration) {
	c := &tieredCache{ttl: ttl, maxEntries: maxEntries, cleanupInterval: cleanupInterval}

	if redisURL != "" {
		opts, err := redis.ParseURL(redisURL)
		if err != nil {
			slog.Warn("cache: invalid redis URL, L2 disabled", slog.Any("error", err))
		} else {
			rdb := redis.NewClient(opts)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := rdb.Ping(ctx).Err(); err != nil {
				slog.Warn("cache: redis unreachable, L2 disabled", slog.Any("error", err))
			} else {
				c.rdb = rdb
				slog.Info("cache: L2 redis connected", slog.String("addr", opts.Addr))
			}
		}
	}

	searchCache = c
	slog.Info("cache: initialized", slog.Duration("ttl", ttl), slog.Bool("redis", c.rdb != nil), slog.Int("max_entries", maxEntries))

	// Start L1 cleanup goroutine
	go c.cleanupLoop()
}

// CacheKey builds a deterministic cache key from parts.
func CacheKey(parts ...string) string {
	joined := strings.Join(parts, "|")
	hash := sha256.Sum256([]byte(joined))
	return fmt.Sprintf("gs:%x", hash[:12]) // 24-char hex prefix
}

// CacheGet tries L1, then L2. On L2 hit, populates L1.
func CacheGet(ctx context.Context, key string) (SmartSearchOutput, bool) {
	if searchCache == nil {
		cacheMisses.Add(1)
		return SmartSearchOutput{}, false
	}

	// L1 check
	if val, ok := searchCache.l1.Load(key); ok {
		entry := val.(*cacheEntry)
		if time.Now().Before(entry.expiresAt) {
			var out SmartSearchOutput
			if json.Unmarshal(entry.data, &out) == nil {
				slog.Debug("cache: L1 hit", slog.String("key", key))
				cacheHits.Add(1)
				return out, true
			}
		}
		searchCache.l1.Delete(key) // expired or corrupt
	}

	// L2 check
	if searchCache.rdb != nil {
		data, err := searchCache.rdb.Get(ctx, key).Bytes()
		if err == nil {
			var out SmartSearchOutput
			if json.Unmarshal(data, &out) == nil {
				slog.Debug("cache: L2 hit", slog.String("key", key))
				cacheHits.Add(1)
				// Populate L1
				searchCache.l1.Store(key, &cacheEntry{
					data:      data,
					expiresAt: time.Now().Add(searchCache.ttl),
				})
				return out, true
			}
		}
	}

	cacheMisses.Add(1)
	return SmartSearchOutput{}, false
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

	// Evict if needed before adding
	searchCache.evictIfNeeded()

	// L1
	searchCache.l1.Store(key, &cacheEntry{
		data:      data,
		expiresAt: time.Now().Add(searchCache.ttl),
	})

	// L2
	if searchCache.rdb != nil {
		if err := searchCache.rdb.Set(ctx, key, data, searchCache.ttl).Err(); err != nil {
			slog.Debug("cache: L2 set failed", slog.Any("error", err))
		}
	}
}

// CacheStats returns current cache hit/miss counters.
func CacheStats() (hits, misses int64) {
	return cacheHits.Load(), cacheMisses.Load()
}

// evictIfNeeded removes entries when L1 exceeds maxEntries.
// Removes expired entries first, then oldest entries if still over limit.
func (c *tieredCache) evictIfNeeded() {
	if c.maxEntries <= 0 {
		return
	}

	// Count entries
	count := 0
	c.l1.Range(func(_, _ any) bool {
		count++
		return true
	})

	if count < c.maxEntries {
		return
	}

	// Phase 1: remove expired
	now := time.Now()
	c.l1.Range(func(key, val any) bool {
		if entry, ok := val.(*cacheEntry); ok && now.After(entry.expiresAt) {
			c.l1.Delete(key)
			count--
		}
		return count >= c.maxEntries
	})

	if count < c.maxEntries {
		return
	}

	// Phase 2: remove oldest entries until under limit
	var oldest struct {
		key any
		at  time.Time
	}
	for count >= c.maxEntries {
		oldest.key = nil
		oldest.at = time.Now().Add(time.Hour) // far future
		c.l1.Range(func(key, val any) bool {
			if entry, ok := val.(*cacheEntry); ok {
				// Earlier expiry = older entry (since expiry = createdAt + ttl)
				if entry.expiresAt.Before(oldest.at) {
					oldest.key = key
					oldest.at = entry.expiresAt
				}
			}
			return true
		})
		if oldest.key == nil {
			break
		}
		c.l1.Delete(oldest.key)
		count--
	}
}

// CacheGetJobDetails retrieves cached job details by URL.
func CacheGetJobDetails(ctx context.Context, jobURL string) (string, bool) {
	key := CacheKey("jd", jobURL)
	if searchCache == nil {
		return "", false
	}

	// L1 check
	if val, ok := searchCache.l1.Load(key); ok {
		entry := val.(*cacheEntry)
		if time.Now().Before(entry.expiresAt) {
			cacheHits.Add(1)
			return string(entry.data), true
		}
		searchCache.l1.Delete(key)
	}

	// L2 check
	if searchCache.rdb != nil {
		data, err := searchCache.rdb.Get(ctx, key).Bytes()
		if err == nil {
			cacheHits.Add(1)
			searchCache.l1.Store(key, &cacheEntry{
				data:      data,
				expiresAt: time.Now().Add(searchCache.ttl),
			})
			return string(data), true
		}
	}

	cacheMisses.Add(1)
	return "", false
}

// CacheSetJobDetails stores job details by URL.
func CacheSetJobDetails(ctx context.Context, jobURL, details string) {
	if searchCache == nil {
		return
	}
	key := CacheKey("jd", jobURL)
	data := []byte(details)

	searchCache.evictIfNeeded()

	searchCache.l1.Store(key, &cacheEntry{
		data:      data,
		expiresAt: time.Now().Add(searchCache.ttl),
	})

	if searchCache.rdb != nil {
		searchCache.rdb.Set(ctx, key, data, searchCache.ttl)
	}
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

// cleanupLoop periodically removes expired L1 entries.
func (c *tieredCache) cleanupLoop() {
	interval := c.cleanupInterval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		c.l1.Range(func(key, val any) bool {
			if entry, ok := val.(*cacheEntry); ok && now.After(entry.expiresAt) {
				c.l1.Delete(key)
			}
			return true
		})
	}
}
