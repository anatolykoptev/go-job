package engine

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestCacheKey(t *testing.T) {
	t.Run("deterministic", func(t *testing.T) {
		k1 := CacheKey("smart_search", "golang context")
		k2 := CacheKey("smart_search", "golang context")
		if k1 != k2 {
			t.Errorf("CacheKey not deterministic: %q != %q", k1, k2)
		}
	})

	t.Run("different inputs differ", func(t *testing.T) {
		k1 := CacheKey("smart_search", "golang")
		k2 := CacheKey("smart_search", "python")
		if k1 == k2 {
			t.Errorf("different inputs produced same key: %q", k1)
		}
	})

	t.Run("has prefix", func(t *testing.T) {
		k := CacheKey("test")
		if k[:3] != "gs:" {
			t.Errorf("expected gs: prefix, got %q", k[:3])
		}
	})
}

func TestCacheGetSet(t *testing.T) {
	// Init minimal cache (no Redis)
	InitCache("", 1*time.Minute, 100, 5*time.Minute)

	ctx := context.Background()
	key := CacheKey("test", "round-trip")

	// Miss
	_, ok := CacheGet(ctx, key)
	if ok {
		t.Error("expected cache miss on empty cache")
	}

	// Set
	val := SmartSearchOutput{Query: "test", Answer: "hello"}
	CacheSet(ctx, key, val)

	// Hit
	got, ok := CacheGet(ctx, key)
	if !ok {
		t.Fatal("expected cache hit after set")
	}
	if got.Answer != "hello" {
		t.Errorf("got answer %q, want %q", got.Answer, "hello")
	}
}

func TestCacheExpiration(t *testing.T) {
	// Init with very short TTL
	InitCache("", 1*time.Millisecond, 100, 5*time.Minute)

	ctx := context.Background()
	key := CacheKey("test", "expiry")

	CacheSet(ctx, key, SmartSearchOutput{Answer: "temp"})
	time.Sleep(5 * time.Millisecond)

	_, ok := CacheGet(ctx, key)
	if ok {
		t.Error("expected cache miss after TTL expiry")
	}
}

func TestCacheEviction(t *testing.T) {
	// maxEntries=3
	InitCache("", 1*time.Minute, 3, 5*time.Minute)
	ctx := context.Background()

	// Add 5 entries
	for i := 0; i < 5; i++ {
		key := CacheKey("evict", fmt.Sprintf("item-%d", i))
		CacheSet(ctx, key, SmartSearchOutput{Answer: fmt.Sprintf("v%d", i)})
	}

	// Count L1 entries
	count := 0
	searchCache.l1.Range(func(_, _ any) bool {
		count++
		return true
	})
	if count > 3 {
		t.Errorf("expected at most 3 entries after eviction, got %d", count)
	}
}

func TestCacheStats(t *testing.T) {
	InitCache("", 1*time.Minute, 100, 5*time.Minute)
	// Reset counters
	cacheHits.Store(0)
	cacheMisses.Store(0)

	ctx := context.Background()
	key := CacheKey("stats", "test")

	// Miss
	CacheGet(ctx, key)
	hits, misses := CacheStats()
	_ = hits
	if misses != 1 {
		t.Errorf("misses = %d, want 1", misses)
	}

	// Set and hit
	CacheSet(ctx, key, SmartSearchOutput{Answer: "x"})
	CacheGet(ctx, key)

	hits, misses = CacheStats()
	if hits != 1 {
		t.Errorf("hits = %d, want 1", hits)
	}
	if misses != 1 {
		t.Errorf("misses = %d, want 1", misses)
	}
}
