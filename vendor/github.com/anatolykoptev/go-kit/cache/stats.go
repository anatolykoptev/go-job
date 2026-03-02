package cache

// Stats holds cache statistics.
type Stats struct {
	L1Hits    int64
	L1Misses  int64
	L1Size    int
	L2Hits    int64
	L2Misses  int64
	L2Errors  int64
	Evictions int64
	HitRatio  float64
}

// Stats returns a snapshot of cache statistics.
func (c *Cache) Stats() Stats {
	hits := c.hits.Load()
	misses := c.misses.Load()
	l2h := c.l2hits.Load()
	l2m := c.l2misses.Load()
	l2e := c.l2errors.Load()
	totalHits := hits + l2h
	totalMisses := misses + l2m
	var ratio float64
	if total := totalHits + totalMisses; total > 0 {
		ratio = float64(totalHits) / float64(total)
	}
	c.mu.Lock()
	size := len(c.items)
	c.mu.Unlock()
	return Stats{
		L1Hits:    hits,
		L1Misses:  misses,
		L1Size:    size,
		L2Hits:    l2h,
		L2Misses:  l2m,
		L2Errors:  l2e,
		Evictions: c.evictions.Load(),
		HitRatio:  ratio,
	}
}
