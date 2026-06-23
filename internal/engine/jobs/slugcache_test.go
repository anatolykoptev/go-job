package jobs

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlugCache_GetMergeEvict(t *testing.T) {
	sc := NewSlugCache("")

	assert.Empty(t, sc.Get("lever"))

	sc.Merge(context.Background(), "lever", []string{"acme", "palantir"})
	got := sc.Get("lever")
	assert.ElementsMatch(t, []string{"acme", "palantir"}, got)

	sc.Evict("lever", "acme", "board_404")
	got = sc.Get("lever")
	assert.Equal(t, []string{"palantir"}, got)
	assert.NotContains(t, got, "acme")
}

func TestSlugCache_TTLExpiry(t *testing.T) {
	sc := NewSlugCache("")
	sc.ttl = 10 * time.Millisecond

	sc.Merge(context.Background(), "lever", []string{"acme"})
	require.NotEmpty(t, sc.Get("lever"))

	time.Sleep(25 * time.Millisecond)
	assert.Empty(t, sc.Get("lever"), "slug must expire after TTL")
}

func TestSlugCache_404Eviction_MetricIncrement(t *testing.T) {
	sc := NewSlugCache("")
	sc.Merge(context.Background(), "lever", []string{"dead-company"})

	before := engine.GetMetrics()
	sc.Evict("lever", "dead-company", "board_404")
	after := engine.GetMetrics()

	key := engine.MetricSlugCacheEvictions + "{platform=lever,reason=board_404}"
	delta := after[key] - before[key]
	assert.Equal(t, int64(1), delta,
		"board_404 eviction counter must increment by 1")

	assert.Empty(t, sc.Get("lever"), "evicted slug must be absent from cache")
}

func TestSlugCache_Race(t *testing.T) {
	sc := NewSlugCache("")
	done := make(chan struct{})

	for i := 0; i < 5; i++ {
		n := i
		go func() {
			sc.Merge(context.Background(), "lever", []string{"slug" + strconv.Itoa(n)})
		}()
	}
	go func() {
		for j := 0; j < 20; j++ {
			_ = sc.Get("lever")
		}
		close(done)
	}()
	go func() {
		sc.Evict("lever", "slug0", "board_404")
	}()
	<-done
}

func TestSlugCache_MaxSize_LRU(t *testing.T) {
	sc := NewSlugCache("")
	sc.maxSize = 3

	sc.Merge(context.Background(), "lever", []string{"a", "b", "c"})
	// Add a 4th slug — "a" or oldest should be evicted
	sc.Merge(context.Background(), "lever", []string{"d"})

	got := sc.Get("lever")
	assert.LessOrEqual(t, len(got), 3, "cache must not exceed maxSize")
}

func TestSlugCache_MaxSize_LRU_EmitsLRUReason(t *testing.T) {
	// Verify LRU-eviction uses reason="lru" not "ttl"
	// (prevents future conflation on dashboards).
	sc := NewSlugCache("")
	sc.maxSize = 2

	before := engine.GetMetrics()
	sc.Merge(context.Background(), "lever", []string{"x", "y", "z"})
	after := engine.GetMetrics()

	lruKey := engine.MetricSlugCacheEvictions + "{platform=lever,reason=lru}"
	ttlKey := engine.MetricSlugCacheEvictions + "{platform=lever,reason=ttl}"
	assert.Greater(t, after[lruKey]-before[lruKey], int64(0), "LRU eviction must emit reason=lru")
	assert.Equal(t, after[ttlKey]-before[ttlKey], int64(0), "LRU eviction must NOT emit reason=ttl")
}
