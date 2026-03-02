package cache

import "time"

// evict removes one entry from the cache using S3-FIFO policy.
func (c *Cache) evict() bool {
	now := time.Now()

	// Phase 1: evict from small queue.
	for c.small.Len() > 0 {
		front := c.small.Front()
		e := front.Value.(*entry)
		c.small.Remove(front)

		if now.After(e.expiresAt) {
			delete(c.items, e.key)
			c.evictions.Add(1)
			return true
		}

		if e.freq > 0 {
			// Accessed while in small — promote to main.
			e.freq = 0
			e.inMain = true
			e.elem = c.main.PushBack(e)
			continue
		}

		// One-hit wonder — evict to ghost.
		delete(c.items, e.key)
		c.evictions.Add(1)
		c.addToGhost(e.key)
		return true
	}

	// Phase 2: evict from main queue (CLOCK-like second chance).
	limit := c.main.Len()
	for i := 0; i < limit && c.main.Len() > 0; i++ {
		front := c.main.Front()
		e := front.Value.(*entry)
		c.main.Remove(front)

		if now.After(e.expiresAt) {
			delete(c.items, e.key)
			c.evictions.Add(1)
			return true
		}

		if e.freq > 0 {
			e.freq--
			e.elem = c.main.PushBack(e)
			continue
		}

		delete(c.items, e.key)
		c.evictions.Add(1)
		return true
	}

	// Safety: force evict front of main if all had freq > 0.
	if front := c.main.Front(); front != nil {
		e := front.Value.(*entry)
		c.main.Remove(front)
		delete(c.items, e.key)
		c.evictions.Add(1)
		return true
	}

	return false
}

// addToGhost adds a key to the ghost queue, evicting the oldest ghost if full.
func (c *Cache) addToGhost(key string) {
	for len(c.ghostMap) >= c.ghostCap {
		front := c.ghost.Front()
		if front == nil {
			break
		}
		old := front.Value.(string)
		c.ghost.Remove(front)
		delete(c.ghostMap, old)
	}
	elem := c.ghost.PushBack(key)
	c.ghostMap[key] = elem
}

// removeEntry removes an active entry from its queue and the items map.
func (c *Cache) removeEntry(e *entry) {
	if e.inMain {
		c.main.Remove(e.elem)
	} else {
		c.small.Remove(e.elem)
	}
	delete(c.items, e.key)
}

// cleanupLoop periodically removes expired entries from L1.
func (c *Cache) cleanupLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			c.mu.Lock()
			now := time.Now()
			for key, e := range c.items {
				if now.After(e.expiresAt) {
					if e.inMain {
						c.main.Remove(e.elem)
					} else {
						c.small.Remove(e.elem)
					}
					delete(c.items, key)
				}
			}
			c.mu.Unlock()
		}
	}
}
