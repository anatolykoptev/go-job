package jobs

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	gokitcache "github.com/anatolykoptev/go-kit/cache"
	"github.com/anatolykoptev/go_job/internal/engine"
)

const (
	defaultSlugCacheTTL     = 24 * time.Hour
	defaultSlugCacheMaxSize = 200
)

// SlugCache caches discovered ATS company slugs per platform.
// Runtime-populated only — NEVER seeded from checked-in data (PUBLIC repo).
// 24h TTL + 404-eviction prevent stale slugs from poisoning discovery.
type SlugCache interface {
	Get(platform string) []string
	Merge(ctx context.Context, platform string, slugs []string)
	Evict(platform, slug, reason string)
	Sweep()
}

type slugEntry struct {
	Slug     string    `json:"slug"`
	LastSeen time.Time `json:"last_seen"`
}

// inProcessSlugCache is the default SlugCache backed by a sync.Mutex-protected map.
// Optional Redis L2 (via go-kit/cache) survives restarts.
type inProcessSlugCache struct {
	mu      sync.Mutex
	entries map[string][]slugEntry
	ttl     time.Duration
	maxSize int
	l2      *gokitcache.RedisL2
}

// NewSlugCache creates a slug cache. redisURL may be empty (in-process only then).
func NewSlugCache(redisURL string) *inProcessSlugCache {
	ttl := defaultSlugCacheTTL
	if v := os.Getenv("SLUG_CACHE_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			ttl = d
		}
	}
	maxSize := defaultSlugCacheMaxSize
	if v := os.Getenv("SLUG_CACHE_MAX_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxSize = n
		}
	}
	var l2 *gokitcache.RedisL2
	if redisURL != "" {
		l2 = gokitcache.NewRedisL2(redisURL, 0, "gj:sc:")
	}
	return &inProcessSlugCache{
		entries: make(map[string][]slugEntry),
		ttl:     ttl,
		maxSize: maxSize,
		l2:      l2,
	}
}

// Get returns non-expired slugs for platform. Warms from Redis L2 on first miss.
func (c *inProcessSlugCache) Get(platform string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.entries[platform]; !ok && c.l2 != nil {
		if data, err := c.l2.Get(context.Background(), platform); err == nil {
			var entries []slugEntry
			if json.Unmarshal(data, &entries) == nil {
				c.entries[platform] = entries
			}
		}
	}

	now := time.Now()
	var result []string
	for _, e := range c.entries[platform] {
		if now.Sub(e.LastSeen) < c.ttl {
			result = append(result, e.Slug)
		}
	}
	return result
}

// Merge adds slugs to the platform set, refreshing lastSeen. Trims to maxSize LRU.
func (c *inProcessSlugCache) Merge(ctx context.Context, platform string, slugs []string) {
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

	// Trim LRU: sort newest-first, evict tail.
	if len(c.entries[platform]) > c.maxSize {
		sort.Slice(c.entries[platform], func(i, j int) bool {
			return c.entries[platform][i].LastSeen.After(c.entries[platform][j].LastSeen)
		})
		for k := c.maxSize; k < len(c.entries[platform]); k++ {
			engine.IncrSlugCacheEviction(platform, "ttl")
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
		go func() {
			data, err := json.Marshal(snapshot)
			if err != nil {
				return
			}
			if err := c.l2.Set(ctx, platform, data, c.ttl); err != nil {
				slog.Debug("slugcache: L2 persist failed", slog.String("platform", platform), slog.Any("error", err))
			}
		}()
	}
}

// Evict removes a slug from the cache. reason ∈ {ttl, board_404}.
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
		go func() {
			data, err := json.Marshal(snapshot)
			if err != nil {
				return
			}
			if err := c.l2.Set(context.Background(), platform, data, c.ttl); err != nil {
				slog.Debug("slugcache: L2 update after evict failed", slog.Any("error", err))
			}
		}()
	}
}

// Sweep removes expired entries from all platforms.
func (c *inProcessSlugCache) Sweep() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for platform, entries := range c.entries {
		kept := entries[:0]
		for _, e := range entries {
			if now.Sub(e.LastSeen) < c.ttl {
				kept = append(kept, e)
			} else {
				engine.IncrSlugCacheEviction(platform, "ttl")
			}
		}
		c.entries[platform] = kept
	}
}

//nolint:gochecknoglobals // package-level singleton, set once at startup
var globalSlugCache SlugCache

// SetSlugCache wires the runtime slug cache at startup (called from main.go).
func SetSlugCache(sc SlugCache) { globalSlugCache = sc }

// GetSlugCache returns the current slug cache (may be nil).
func GetSlugCache() SlugCache { return globalSlugCache }
