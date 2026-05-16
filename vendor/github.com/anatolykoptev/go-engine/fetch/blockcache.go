package fetch

import (
	"sync"
	"time"
)

// defaultBlockTTL is how long a host is considered blocked after first detection.
const defaultBlockTTL = 10 * time.Minute

// defaultBlockCacheCap is the maximum number of hosts tracked.
const defaultBlockCacheCap = 1024

// DirectBlockCache is an in-process cache of hosts known to block
// direct (no-proxy) requests. Repeat calls within the TTL skip the direct
// attempt and go straight to proxy.
//
// Eviction: FIFO (insertion-order), not access-recency LRU. When capacity is
// reached, the oldest-inserted entry is removed regardless of recent access.
// A map + insertion-order slice is used; a full LRU heap would add complexity
// without meaningful benefit at 1024 entries.
type DirectBlockCache struct {
	mu    sync.Mutex
	items map[string]time.Time // host → unblock-at
	order []string             // insertion order for eviction
	ttl   time.Duration
	cap   int
}

// NewDirectBlockCache creates a new DirectBlockCache with the given TTL and capacity.
func NewDirectBlockCache(ttl time.Duration, cap int) *DirectBlockCache {
	if ttl <= 0 {
		ttl = defaultBlockTTL
	}
	if cap <= 0 {
		cap = defaultBlockCacheCap
	}
	return &DirectBlockCache{
		items: make(map[string]time.Time, cap),
		order: make([]string, 0, cap),
		ttl:   ttl,
		cap:   cap,
	}
}

// IsBlocked reports whether host is currently marked as blocking direct requests.
func (c *DirectBlockCache) IsBlocked(host string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	unblockAt, ok := c.items[host]
	if !ok {
		return false
	}
	if time.Now().After(unblockAt) {
		// TTL expired — evict.
		delete(c.items, host)
		return false
	}
	return true
}

// Mark records host as blocking direct requests for the configured TTL.
func (c *DirectBlockCache) Mark(host string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.items[host]; !exists {
		// Only track insertion order for new entries.
		if len(c.items) >= c.cap {
			c.evictOldest()
		}
		c.order = append(c.order, host)
	}
	c.items[host] = time.Now().Add(c.ttl)
}

// Len returns the current number of tracked hosts.
func (c *DirectBlockCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// evictOldest removes the oldest entry by insertion order. Must be called with mu held.
func (c *DirectBlockCache) evictOldest() {
	for len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		if _, ok := c.items[oldest]; ok {
			delete(c.items, oldest)
			return
		}
	}
}
