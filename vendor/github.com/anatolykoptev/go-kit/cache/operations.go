package cache

import (
	"container/list"
	"context"
	"errors"
	"log/slog"
	"time"
)

// Get retrieves a value from L1 (then L2 if configured). Returns nil, false on miss.
func (c *Cache) Get(ctx context.Context, key string) ([]byte, bool) {
	c.mu.Lock()

	e, ok := c.items[key]
	if ok && !time.Now().After(e.expiresAt) {
		if e.freq < maxFreq {
			e.freq++
		}
		data := e.data
		c.mu.Unlock()
		c.hits.Add(1)
		return data, true
	}

	// L1 miss or expired.
	if ok {
		c.removeEntry(e)
	}
	c.mu.Unlock()

	// Try L2.
	if c.l2 != nil {
		data, err := c.l2.Get(ctx, key)
		if err == nil {
			c.l2hits.Add(1)
			c.Set(ctx, key, data)
			return data, true
		}
		if errors.Is(err, ErrCacheMiss) {
			c.l2misses.Add(1)
		} else {
			c.l2errors.Add(1)
		}
	}

	c.misses.Add(1)
	return nil, false
}

// Set stores a value in L1 (and L2 if configured) using global TTLs.
func (c *Cache) Set(ctx context.Context, key string, data []byte) {
	c.setInternal(ctx, key, data, c.cfg.L1TTL, c.cfg.L2TTL)
}

// SetWithTTL stores a value with a custom TTL for both L1 and L2.
// L1 applies jitter to the provided TTL; L2 uses it directly.
// If ttl <= 0, falls back to global TTLs (same as Set).
func (c *Cache) SetWithTTL(ctx context.Context, key string, data []byte, ttl time.Duration) {
	if ttl <= 0 {
		c.Set(ctx, key, data)
		return
	}
	c.setInternal(ctx, key, data, ttl, ttl)
}

func (c *Cache) setInternal(ctx context.Context, key string, data []byte, l1TTL, l2TTL time.Duration) {
	c.mu.Lock()

	// Update existing entry.
	if e, ok := c.items[key]; ok {
		e.data = data
		e.expiresAt = time.Now().Add(c.jitteredTTL(l1TTL))
		c.mu.Unlock()
		// Write-through to L2 (best-effort).
		if c.l2 != nil {
			if err := c.l2.Set(ctx, key, data, l2TTL); err != nil {
				slog.Debug("cache: L2 set failed", slog.Any("error", err))
			}
		}
		return
	}

	// Evict until under capacity.
	for len(c.items) >= c.cfg.L1MaxItems {
		if !c.evict() {
			break
		}
	}

	// Check ghost for frequency boost.
	var initFreq uint8
	if ge, ok := c.ghostMap[key]; ok {
		c.ghost.Remove(ge)
		delete(c.ghostMap, key)
		initFreq = 1 // ghost re-admission boost
	}

	// Insert into small queue.
	e := &entry{
		key:       key,
		data:      data,
		expiresAt: time.Now().Add(c.jitteredTTL(l1TTL)),
		freq:      initFreq,
	}
	e.elem = c.small.PushBack(e)
	c.items[key] = e
	c.mu.Unlock()

	// Write-through to L2 (best-effort).
	if c.l2 != nil {
		if err := c.l2.Set(ctx, key, data, l2TTL); err != nil {
			slog.Debug("cache: L2 set failed", slog.Any("error", err))
		}
	}
}

// Delete removes a key from both L1 and L2.
func (c *Cache) Delete(ctx context.Context, key string) {
	c.mu.Lock()
	if e, ok := c.items[key]; ok {
		c.removeEntry(e)
	}
	c.mu.Unlock()

	// Delete from L2 (best-effort).
	if c.l2 != nil {
		if err := c.l2.Del(ctx, key); err != nil {
			slog.Debug("cache: L2 del failed", slog.Any("error", err))
		}
	}
}

// GetOrLoad returns the value for key, loading it via loader on cache miss.
// Concurrent loads for the same key are deduplicated (singleflight).
// The loaded value is stored in L1.
func (c *Cache) GetOrLoad(ctx context.Context, key string, loader func(context.Context) ([]byte, error)) ([]byte, error) {
	if data, ok := c.Get(ctx, key); ok {
		return data, nil
	}

	data, err := c.flight.do(key, func() ([]byte, error) {
		return loader(ctx)
	})
	if err != nil {
		return nil, err
	}

	c.Set(ctx, key, data)
	return data, nil
}

// GetOrLoadWithTTL is like GetOrLoad but stores the loaded value with a custom TTL.
func (c *Cache) GetOrLoadWithTTL(ctx context.Context, key string, ttl time.Duration,
	loader func(context.Context) ([]byte, error),
) ([]byte, error) {
	if data, ok := c.Get(ctx, key); ok {
		return data, nil
	}

	data, err := c.flight.do(key, func() ([]byte, error) {
		return loader(ctx)
	})
	if err != nil {
		return nil, err
	}

	c.SetWithTTL(ctx, key, data, ttl)
	return data, nil
}

// Clear removes all entries from L1 and returns the number cleared.
// L2 is not affected.
func (c *Cache) Clear() int {
	c.mu.Lock()
	n := len(c.items)
	c.items = make(map[string]*entry)
	c.small.Init()
	c.main.Init()
	c.ghost.Init()
	c.ghostMap = make(map[string]*list.Element)
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
