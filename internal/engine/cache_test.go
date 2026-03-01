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

	t.Run("non-empty", func(t *testing.T) {
		k := CacheKey("test")
		if len(k) == 0 {
			t.Error("expected non-empty key")
		}
	})
}

func TestCacheGetSet(t *testing.T) {
	InitCache("", 1*time.Minute, 100, 5*time.Minute)
	defer searchCache.Close()

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
	InitCache("", 1*time.Millisecond, 100, 5*time.Minute)
	defer searchCache.Close()

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
	InitCache("", 1*time.Minute, 3, 5*time.Minute)
	defer searchCache.Close()

	ctx := context.Background()

	// Add 5 entries
	for i := 0; i < 5; i++ {
		key := CacheKey("evict", fmt.Sprintf("item-%d", i))
		CacheSet(ctx, key, SmartSearchOutput{Answer: fmt.Sprintf("v%d", i)})
	}

	s := searchCache.Stats()
	if s.L1Size > 3 {
		t.Errorf("expected at most 3 entries after eviction, got %d", s.L1Size)
	}
}

func TestCacheStats(t *testing.T) {
	InitCache("", 1*time.Minute, 100, 5*time.Minute)
	defer searchCache.Close()

	ctx := context.Background()
	key := CacheKey("stats", "test")

	// Miss
	CacheGet(ctx, key)
	hits, misses := CacheStats()
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

func TestCacheJobDetails(t *testing.T) {
	InitCache("", 1*time.Minute, 100, 5*time.Minute)
	defer searchCache.Close()

	ctx := context.Background()

	// Miss
	_, ok := CacheGetJobDetails(ctx, "https://example.com/job/123")
	if ok {
		t.Error("expected miss on empty cache")
	}

	// Set and hit
	CacheSetJobDetails(ctx, "https://example.com/job/123", "Senior Go Developer")
	got, ok := CacheGetJobDetails(ctx, "https://example.com/job/123")
	if !ok {
		t.Fatal("expected hit after set")
	}
	if got != "Senior Go Developer" {
		t.Errorf("got %q, want %q", got, "Senior Go Developer")
	}
}

func TestCacheLoadStoreJSON(t *testing.T) {
	InitCache("", 1*time.Minute, 100, 5*time.Minute)
	defer searchCache.Close()

	ctx := context.Background()
	key := CacheKey("json", "test")

	type payload struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}

	// Miss
	_, ok := CacheLoadJSON[payload](ctx, key)
	if ok {
		t.Error("expected miss on empty cache")
	}

	// Store and load
	CacheStoreJSON(ctx, key, "test query", payload{Name: "foo", Count: 42})
	got, ok := CacheLoadJSON[payload](ctx, key)
	if !ok {
		t.Fatal("expected hit after store")
	}
	if got.Name != "foo" || got.Count != 42 {
		t.Errorf("got %+v, want {foo 42}", got)
	}
}
