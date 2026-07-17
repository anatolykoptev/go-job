package jobs

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"sync"
	"time"

	gokitcache "github.com/anatolykoptev/go-kit/cache"
	"github.com/anatolykoptev/go-kit/env"
	"github.com/anatolykoptev/go_job/internal/engine"
)

const (
	defaultSlugCacheTTL     = 24 * time.Hour
	defaultSlugCacheMaxSize = 200
)

// SlugCache caches discovered ATS company slugs per platform.
// Runtime-populated only — NEVER seeded from checked-in data (PUBLIC repo).
// TTL expiry is enforced lazily on Get (filter-on-read) + trimmed at LRU-eviction
// time inside Merge. No background Sweep goroutine — maxSize bounds memory.
// 404-eviction via Evict ensures stale boards are removed immediately.
type SlugCache interface {
	Get(platform string) []string
	Merge(ctx context.Context, platform string, slugs []string)
	Evict(platform, slug, reason string)
}

type slugEntry struct {
	Slug     string    `json:"slug"`
	LastSeen time.Time `json:"last_seen"`
}

// inProcessSlugCache is the default SlugCache backed by a sync.Mutex-protected map.
// Optional Redis L2 (via go-kit/cache) survives restarts.
//
// L2 warmup uses double-checked locking via per-platform sync.Once to avoid
// holding the mutex during the Redis round-trip (PF-7 fix). The once map
// itself is guarded by mu — only the check+create of the Once is under the
// lock, not the Redis call.
//
// L2 writes go through a bounded worker pool (PF-11 fix) instead of
// fire-and-forget goroutines, preventing goroutine pile-up when Redis is
// slow or down during high slug churn.
type inProcessSlugCache struct {
	mu       sync.Mutex
	entries  map[string][]slugEntry
	warmOnce map[string]*sync.Once
	ttl      time.Duration
	maxSize  int
	l2       *gokitcache.RedisL2
	l2Pool   chan l2WriteJob // bounded pool for L2 writes
}

// l2WriteJob is a unit of work for the L2 writer pool.
type l2WriteJob struct {
	platform string
	data     []byte
	ttl      time.Duration
}

// NewSlugCache creates a slug cache. redisURL may be empty (in-process only then).
// Panics if SLUG_CACHE_TTL or SLUG_CACHE_MAX_SIZE is set to an invalid value
// (PF-8 fix: fail-fast on config errors instead of silent default fallback).
func NewSlugCache(redisURL string) *inProcessSlugCache {
	ttl := env.MustDuration("SLUG_CACHE_TTL", defaultSlugCacheTTL)
	maxSize := env.MustInt("SLUG_CACHE_MAX_SIZE", defaultSlugCacheMaxSize)
	var l2 *gokitcache.RedisL2
	if redisURL != "" {
		l2 = gokitcache.NewRedisL2(redisURL, 0, "gj:sc:")
	}
	c := &inProcessSlugCache{
		entries:  make(map[string][]slugEntry),
		warmOnce: make(map[string]*sync.Once),
		ttl:      ttl,
		maxSize:  maxSize,
		l2:       l2,
	}
	if l2 != nil {
		// PF-11 fix: bounded worker pool for L2 writes (10 workers).
		// Prevents goroutine pile-up when Redis is slow/down.
		const l2PoolSize = 10
		c.l2Pool = make(chan l2WriteJob, l2PoolSize)
		for range l2PoolSize {
			go c.l2Writer()
		}
	}
	return c
}

// Get returns non-expired slugs for platform. Warms from Redis L2 on first miss
// using double-checked locking (PF-7 fix): the mutex is only held briefly to
// get-or-create the per-platform sync.Once, then released during the Redis
// round-trip, then re-acquired to populate the entries map.
func (c *inProcessSlugCache) Get(platform string) []string {
	// Fast path: entries already populated (no L2 warmup needed).
	c.mu.Lock()
	if _, ok := c.entries[platform]; ok {
		result := c.filterUnexpired(platform)
		c.mu.Unlock()
		return result
	}
	c.mu.Unlock()

	// Slow path: first miss for this platform — warm from L2.
	if c.l2 != nil {
		c.warmFromL2(platform)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	return c.filterUnexpired(platform)
}

// warmFromL2 fetches slugs from Redis and populates entries. Uses
// double-checked locking via sync.Once so concurrent Get calls for the same
// platform don't all hit Redis, and the mutex is NOT held during the round-trip.
func (c *inProcessSlugCache) warmFromL2(platform string) {
	c.mu.Lock()
	once, ok := c.warmOnce[platform]
	if !ok {
		once = &sync.Once{}
		c.warmOnce[platform] = once
	}
	c.mu.Unlock()

	once.Do(func() {
		data, err := c.l2.Get(context.Background(), platform)
		if err != nil {
			return
		}
		var entries []slugEntry
		if json.Unmarshal(data, &entries) != nil {
			return
		}
		c.mu.Lock()
		// Double-check: another Merge may have populated entries while we
		// were fetching from Redis. Only write if still absent.
		if _, ok := c.entries[platform]; !ok {
			c.entries[platform] = entries
		}
		c.mu.Unlock()
	})
}

// filterUnexpired returns non-expired slugs for platform. Caller MUST hold c.mu.
func (c *inProcessSlugCache) filterUnexpired(platform string) []string {
	now := time.Now()
	var result []string
	for _, e := range c.entries[platform] {
		if now.Sub(e.LastSeen) < c.ttl {
			result = append(result, e.Slug)
		}
	}
	return result
}

// l2Writer is a bounded worker that processes L2 write jobs from the pool channel.
// PF-11 fix: replaces fire-and-forget goroutines that could pile up under Redis slowness.
// OBS-3 fix: L2 write failures promoted from Debug to Warn + metric increment.
func (c *inProcessSlugCache) l2Writer() {
	for job := range c.l2Pool {
		if err := c.l2.Set(context.Background(), job.platform, job.data, job.ttl); err != nil {
			slog.Warn("slugcache: L2 persist failed",
				slog.String("platform", job.platform),
				slog.Any("error", err))
			engine.IncrSlugCacheL2WriteError()
		}
	}
}

// submitL2Write enqueues an L2 write job to the bounded pool.
// Non-blocking: if the pool is full, the write is dropped (cache is best-effort).
func (c *inProcessSlugCache) submitL2Write(platform string, snapshot []slugEntry) {
	if c.l2Pool == nil {
		return
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return
	}
	select {
	case c.l2Pool <- l2WriteJob{platform: platform, data: data, ttl: c.ttl}:
	default:
		slog.Debug("slugcache: L2 write pool full, dropping write",
			slog.String("platform", platform))
	}
}

// Merge adds slugs to the platform set, refreshing lastSeen. Trims to maxSize LRU.
// ctx is accepted for interface compatibility; L2 writes use context.Background()
// so cache persistence survives request cancellation.
func (c *inProcessSlugCache) Merge(_ context.Context, platform string, slugs []string) {
	if len(slugs) == 0 {
		return
	}
	c.mu.Lock()

	now := time.Now()
	idx := make(map[string]int, len(c.entries[platform]))
	for i, e := range c.entries[platform] {
		idx[e.Slug] = i
	}
	for _, s := range slugs {
		if i, ok := idx[s]; ok {
			c.entries[platform][i].LastSeen = now
		} else {
			c.entries[platform] = append(c.entries[platform], slugEntry{Slug: s, LastSeen: now})
			idx[s] = len(c.entries[platform]) - 1
		}
	}

	// Trim LRU: sort newest-first, evict tail. Reason="lru" (size-pressure),
	// distinct from reason="ttl" (age-based expiry filtered on Get) and
	// reason="board_404" (HTTP 404 from board-fetch).
	if len(c.entries[platform]) > c.maxSize {
		sort.Slice(c.entries[platform], func(i, j int) bool {
			return c.entries[platform][i].LastSeen.After(c.entries[platform][j].LastSeen)
		})
		for k := c.maxSize; k < len(c.entries[platform]); k++ {
			engine.IncrSlugCacheEviction(platform, "lru")
			slog.Debug("slugcache: LRU evict",
				slog.String("platform", platform),
				slog.String("slug", c.entries[platform][k].Slug))
		}
		c.entries[platform] = c.entries[platform][:c.maxSize]
	}

	size := len(c.entries[platform])
	snapshot := make([]slugEntry, size)
	copy(snapshot, c.entries[platform])
	c.mu.Unlock()

	slog.Debug("slugcache: merged", slog.String("platform", platform), slog.Int("size", size))

	if c.l2 != nil {
		// PF-11 fix: submit to bounded worker pool instead of fire-and-forget goroutine.
		c.submitL2Write(platform, snapshot)
	}
}

// Evict removes a slug from the cache. reason ∈ {board_404}.
func (c *inProcessSlugCache) Evict(platform, slug, reason string) {
	c.mu.Lock()
	entries := c.entries[platform]
	for i, e := range entries {
		if e.Slug == slug {
			c.entries[platform] = append(entries[:i], entries[i+1:]...)
			engine.IncrSlugCacheEviction(platform, reason)
			slog.Debug("slugcache: evicted", slog.String("platform", platform),
				slog.String("slug", slug), slog.String("reason", reason))
			break
		}
	}
	snapshot := make([]slugEntry, len(c.entries[platform]))
	copy(snapshot, c.entries[platform])
	c.mu.Unlock()

	if c.l2 != nil {
		// PF-11 fix: submit to bounded worker pool instead of fire-and-forget goroutine.
		c.submitL2Write(platform, snapshot)
	}
}

//nolint:gochecknoglobals // package-level singleton, set once at startup
var globalSlugCache SlugCache

// SetSlugCache wires the runtime slug cache at startup (called from main.go).
func SetSlugCache(sc SlugCache) { globalSlugCache = sc }

// GetSlugCache returns the current slug cache (may be nil).
func GetSlugCache() SlugCache { return globalSlugCache }
