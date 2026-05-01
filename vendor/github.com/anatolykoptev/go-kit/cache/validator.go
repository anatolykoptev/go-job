package cache

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// Validator inspects a cached value and reports whether it is still fresh
// against external state (file modTime+size, DB row updated_at, ETag, source
// hash, etc.).
//
// Returning true keeps the entry and serves it to the caller. Returning false
// treats the entry as stale: the entry is evicted from L1, a metric is
// incremented, and the call falls through to L2 (or reports a miss when L2
// is not configured).
//
// Implementations MUST be cheap (Validator runs on every L1 hit) and safe
// for concurrent invocation.
type Validator func(cached []byte) bool

// GetIfValid is like Get but additionally calls validator on every L1 hit.
// When validator returns false, the entry is evicted from L1 and the call
// falls through to L2. validator is NOT called on L1 miss or on L2 hit —
// callers wanting external-state validation against L2 should use a
// freshness-bearing key (e.g. include modTime in the cache key).
//
// A nil validator behaves identically to plain Get (no validation).
//
// Use to invalidate against external state: file modTime+size, DB row
// updated_at, ETag, source hash. Returning false → entry treated as stale
// and evicted; cache_validator_evict_total{reason="stale"} is incremented.
func (c *Cache) GetIfValid(ctx context.Context, key string, validator Validator) ([]byte, bool) {
	if validator == nil {
		return c.Get(ctx, key)
	}

	c.mu.Lock()

	e, ok := c.items[key]
	if ok && !time.Now().After(e.expiresAt) {
		// IdleTTL check must happen BEFORE updating lastAccess.
		if c.cfg.IdleTTL > 0 && time.Since(e.lastAccess) > c.cfg.IdleTTL {
			expKey, expData := e.key, e.data
			c.removeEntry(e)
			c.mu.Unlock()
			c.notifyEvict(expKey, expData, EvictExpired)
			c.misses.Add(1)
			return nil, false
		}

		// Snapshot data for validator (run with lock held to avoid races on the
		// entry; the validator must be fast and side-effect-free).
		data := e.data

		if !validator(data) {
			// Stale per external state — evict and treat as miss.
			expKey, expData := e.key, e.data
			c.removeEntry(e)
			c.mu.Unlock()
			c.notifyEvict(expKey, expData, EvictExplicit)
			recordValidatorEvict("stale")
			// Fall through to L2 below — re-acquire path mirrors Get's miss path.
			return c.getL2OrMiss(ctx, key)
		}

		if e.freq < maxFreq {
			e.freq++
		}
		if c.cfg.IdleTTL > 0 {
			e.lastAccess = time.Now()
		}
		c.mu.Unlock()
		c.hits.Add(1)
		return data, true
	}

	// L1 miss or expired.
	var expKey string
	var expData []byte
	wasExpired := ok
	if ok {
		expKey, expData = e.key, e.data
		c.removeEntry(e)
	}
	c.mu.Unlock()
	if wasExpired {
		c.notifyEvict(expKey, expData, EvictExpired)
	}

	return c.getL2OrMiss(ctx, key)
}

// getL2OrMiss is the post-L1-miss tail shared by Get and GetIfValid: try L2
// (when configured), promote on hit to L1, otherwise record a miss.
func (c *Cache) getL2OrMiss(ctx context.Context, key string) ([]byte, bool) {
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
			slog.Debug("cache: L2 get failed", slog.Any("error", err))
		}
	}

	c.misses.Add(1)
	return nil, false
}
