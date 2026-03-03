package cache

import (
	"context"
	"log/slog"
	"time"
)

// addToTagIndex registers key under each tag. Caller must hold c.mu.
func (c *Cache) addToTagIndex(key string, tags []string) {
	for _, tag := range tags {
		keys, ok := c.tagIndex[tag]
		if !ok {
			keys = make(map[string]struct{})
			c.tagIndex[tag] = keys
		}
		keys[key] = struct{}{}
	}
}

// removeFromTagIndex removes key from each tag's set and cleans empty sets.
// Caller must hold c.mu.
func (c *Cache) removeFromTagIndex(key string, tags []string) {
	for _, tag := range tags {
		keys := c.tagIndex[tag]
		if keys == nil {
			continue
		}
		delete(keys, key)
		if len(keys) == 0 {
			delete(c.tagIndex, tag)
		}
	}
}

// updateTags replaces old tags with new tags for a key. Caller must hold c.mu.
func (c *Cache) updateTags(key string, old, new []string) {
	c.removeFromTagIndex(key, old)
	c.addToTagIndex(key, new)
}

// SetWithTags stores a value with associated tags using global TTLs.
func (c *Cache) SetWithTags(ctx context.Context, key string, data []byte, tags []string) {
	c.setInternal(ctx, key, data, c.cfg.L1TTL, c.cfg.L2TTL, tags...)
}

// SetWithTagsAndTTL stores a value with a custom TTL and associated tags.
// If ttl <= 0, falls back to global TTLs.
func (c *Cache) SetWithTagsAndTTL(ctx context.Context, key string, data []byte, ttl time.Duration, tags []string) {
	if ttl <= 0 {
		c.SetWithTags(ctx, key, data, tags)
		return
	}
	c.setInternal(ctx, key, data, ttl, ttl, tags...)
}

// InvalidateByTag removes all entries associated with the given tag from L1
// and L2 (best-effort). Returns the number of entries removed.
func (c *Cache) InvalidateByTag(ctx context.Context, tag string) int {
	c.mu.Lock()
	keys := c.tagIndex[tag]
	if len(keys) == 0 {
		c.mu.Unlock()
		return 0
	}

	// Snapshot keys — removeEntry modifies tagIndex during iteration.
	snapshot := make([]string, 0, len(keys))
	for key := range keys {
		snapshot = append(snapshot, key)
	}

	var batch []evictedEntry
	for _, key := range snapshot {
		e, ok := c.items[key]
		if !ok {
			continue
		}
		if c.cfg.OnEvict != nil {
			batch = append(batch, evictedEntry{key: key, data: e.data, reason: EvictExplicit})
		}
		c.removeEntry(e)
	}
	count := len(batch)
	if count == 0 {
		count = len(snapshot)
	}
	// Clean up tag entry (removeEntry may have already emptied it).
	delete(c.tagIndex, tag)
	c.mu.Unlock()

	c.notifyBatch(batch)

	// L2 cleanup (best-effort).
	if c.l2 != nil {
		for _, key := range snapshot {
			if err := c.l2.Del(ctx, key); err != nil {
				slog.Debug("cache: L2 tag invalidate failed", slog.Any("error", err))
			}
		}
	}

	return count
}

// Tags returns a copy of the tags associated with the given key.
// Returns nil if the key is not in L1.
func (c *Cache) Tags(key string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok || len(e.tags) == 0 {
		return nil
	}
	cp := make([]string, len(e.tags))
	copy(cp, e.tags)
	return cp
}
