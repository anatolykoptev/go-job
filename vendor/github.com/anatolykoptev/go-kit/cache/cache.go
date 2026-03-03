// Package cache provides a tiered L1 (memory) + optional L2 (Redis) cache.
// L1 uses S3-FIFO eviction with 3 queues (small, main, ghost) for high hit rates.
// If RedisURL is empty, operates as L1-only (no external dependencies needed at runtime).
package cache

import (
	"container/list"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"
)

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

// maxFreq is the S3-FIFO frequency counter ceiling (0-3).
const maxFreq = 3

// entry is an item stored in the S3-FIFO cache.
type entry struct {
	key       string
	data      []byte
	expiresAt time.Time
	freq      uint8         // 0-3, S3-FIFO frequency counter
	elem      *list.Element // back-ref in small or main list
	inMain    bool          // false=small, true=main
	tags      []string      // tag-based invalidation groups
}

// Cache is a tiered L1 (memory) + optional L2 (Redis) cache.
// L1 uses the S3-FIFO eviction algorithm with three queues.
type Cache struct {
	cfg Config

	mu       sync.Mutex
	items    map[string]*entry        // all active entries
	small    *list.List               // probation queue (10% capacity)
	main     *list.List               // main queue (90% capacity)
	ghost    *list.List               // ghost queue (evicted keys, no values)
	ghostMap map[string]*list.Element  // ghost key lookups
	tagIndex map[string]map[string]struct{} // tag → set of keys

	smallCap int // 10% of L1MaxItems
	mainCap  int // 90% of L1MaxItems
	ghostCap int // = mainCap

	hits      atomic.Int64
	misses    atomic.Int64
	evictions atomic.Int64
	l2hits    atomic.Int64
	l2misses  atomic.Int64
	l2errors  atomic.Int64

	flight group
	l2     L2 // optional L2 store; nil = L1-only
	done   chan struct{}
}

// New creates a new Cache. If cfg.RedisURL is empty, L2 is disabled.
// Call Close() when done to stop the background cleanup goroutine.
func New(cfg Config) *Cache {
	cfg.applyDefaults()

	smallCap := cfg.L1MaxItems / 10
	if smallCap < 1 {
		smallCap = 1
	}
	mainCap := cfg.L1MaxItems - smallCap

	c := &Cache{
		cfg:      cfg,
		items:    make(map[string]*entry),
		small:    list.New(),
		main:     list.New(),
		ghost:    list.New(),
		ghostMap: make(map[string]*list.Element),
		tagIndex: make(map[string]map[string]struct{}),
		smallCap: smallCap,
		mainCap:  mainCap,
		ghostCap: mainCap,
		done:     make(chan struct{}),
	}

	// Use explicitly provided L2, else try Redis, else nil.
	if cfg.L2 != nil {
		c.l2 = cfg.L2
	} else if cfg.RedisURL != "" {
		// Guard: NewRedisL2 returns nil on failure — must NOT assign nil
		// concrete pointer to interface (Go typed-nil trap causes SIGSEGV).
		if l2 := NewRedisL2(cfg.RedisURL, cfg.RedisDB, cfg.Prefix); l2 != nil {
			c.l2 = l2
		}
	}

	// Background cleanup every 1/10 of TTL, minimum 10s.
	interval := cfg.L1TTL / 10
	if interval < 10*time.Second {
		interval = 10 * time.Second
	}
	go c.cleanupLoop(interval)

	return c
}

func (c *Cache) jitteredTTL(base time.Duration) time.Duration {
	if c.cfg.JitterPercent <= 0 {
		return base
	}
	jitter := int64(float64(base) * c.cfg.JitterPercent)
	if jitter <= 0 {
		return base
	}
	return base + time.Duration(rand.Int64N(2*jitter+1)-jitter)
}

// Clear removes all entries from L1 and returns the number cleared.
// L2 is not affected. OnEvict callbacks are NOT fired (bulk operation).
func (c *Cache) Clear() int {
	c.mu.Lock()
	n := len(c.items)
	c.items = make(map[string]*entry)
	c.small.Init()
	c.main.Init()
	c.ghost.Init()
	c.ghostMap = make(map[string]*list.Element)
	c.tagIndex = make(map[string]map[string]struct{})
	c.mu.Unlock()
	return n
}

// Close stops the background cleanup goroutine and closes L2 if set.
func (c *Cache) Close() {
	select {
	case <-c.done:
	default:
		close(c.done)
	}
	if c.l2 != nil {
		c.l2.Close()
	}
}

