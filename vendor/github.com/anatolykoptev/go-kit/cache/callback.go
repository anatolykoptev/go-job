package cache

// EvictReason indicates why a cache entry was removed.
type EvictReason int

const (
	EvictExpired  EvictReason = iota // TTL elapsed
	EvictCapacity                    // evicted for space (S3-FIFO)
	EvictExplicit                    // Delete() called
)

type evictedEntry struct {
	key    string
	data   []byte
	reason EvictReason
}

func (c *Cache) notifyEvict(key string, data []byte, reason EvictReason) {
	if c.cfg.OnEvict != nil {
		c.cfg.OnEvict(key, data, reason)
	}
}

func (c *Cache) notifyBatch(batch []evictedEntry) {
	if c.cfg.OnEvict == nil {
		return
	}
	for _, e := range batch {
		c.cfg.OnEvict(e.key, e.data, e.reason)
	}
}
