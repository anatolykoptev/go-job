package jobs

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeQueryDiscoverer struct {
	mu      sync.Mutex
	results map[string]engine.SearxngResult
}

func (f *fakeQueryDiscoverer) DiscoverBoardURLs(_ context.Context, query string) ([]engine.SearxngResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.results[query]; ok {
		return []engine.SearxngResult{r}, nil
	}
	return nil, nil
}

type slowFakeDiscoverer struct {
	delay  time.Duration
	result engine.SearxngResult
	calls  atomic.Int64
}

func (s *slowFakeDiscoverer) DiscoverBoardURLs(_ context.Context, _ string) ([]engine.SearxngResult, error) {
	s.calls.Add(1)
	time.Sleep(s.delay)
	return []engine.SearxngResult{s.result}, nil
}

func resetSlugCache(t *testing.T) {
	t.Helper()
	prev := globalSlugCache
	t.Cleanup(func() { globalSlugCache = prev })
}

func TestUnionDiscoversMoreThanSingle(t *testing.T) {
	resetATSDiscoverer(t)
	resetSlugCache(t)
	SetSlugCache(nil)

	variants := discoveryVariants(engine.DiscoveryPlatformLever, "golang", "")
	require.GreaterOrEqual(t, len(variants), 2, "need >=2 variants for union to beat single")

	results := make(map[string]engine.SearxngResult, len(variants))
	for i, v := range variants {
		results[v] = engine.SearxngResult{
			URL:   "https://jobs.lever.co/company" + strconv.Itoa(i),
			Title: "Company" + strconv.Itoa(i),
		}
	}
	ATSDiscoverer = &fakeQueryDiscoverer{results: results}

	got := unionDiscoverSlugs(context.Background(), engine.DiscoveryPlatformLever, "golang", "", extractLeverSlugs)
	assert.Greater(t, len(got), 1,
		"union must yield >1 slug when each variant maps to a distinct slug")
}

func TestUnionParallelism(t *testing.T) {
	resetATSDiscoverer(t)
	resetSlugCache(t)
	SetSlugCache(nil)

	const variantDelay = 60 * time.Millisecond
	slow := &slowFakeDiscoverer{
		delay:  variantDelay,
		result: engine.SearxngResult{URL: "https://jobs.lever.co/acme", Title: "Acme"},
	}
	ATSDiscoverer = slow

	variants := discoveryVariants(engine.DiscoveryPlatformLever, "golang", "")
	require.GreaterOrEqual(t, len(variants), 2)

	start := time.Now()
	_ = unionDiscoverSlugs(context.Background(), engine.DiscoveryPlatformLever, "golang", "", extractLeverSlugs)
	elapsed := time.Since(start)

	budget := time.Duration(float64(variantDelay) * 2.5)
	assert.LessOrEqual(t, elapsed, budget,
		"variants must run in parallel; budget %v, got %v", budget, elapsed)
}

func TestCacheStickiness(t *testing.T) {
	resetATSDiscoverer(t)
	resetSlugCache(t)

	sc := NewSlugCache("")
	SetSlugCache(sc)

	variants := discoveryVariants(engine.DiscoveryPlatformLever, "golang", "")
	results := make(map[string]engine.SearxngResult)
	for _, v := range variants {
		results[v] = engine.SearxngResult{
			URL: "https://jobs.lever.co/acme", Title: "Acme",
		}
	}
	ATSDiscoverer = &fakeQueryDiscoverer{results: results}
	got1 := unionDiscoverSlugs(context.Background(), engine.DiscoveryPlatformLever, "golang", "", extractLeverSlugs)
	require.NotEmpty(t, got1, "first call must find slugs")
	require.Contains(t, got1, "acme")

	// Wait for async L2 Merge goroutine to finish (no Redis in tests, no-op)
	time.Sleep(10 * time.Millisecond)

	ATSDiscoverer = &fakeQueryDiscoverer{results: nil}
	got2 := unionDiscoverSlugs(context.Background(), engine.DiscoveryPlatformLever, "golang", "", extractLeverSlugs)

	assert.Contains(t, got2, "acme",
		"cached slug 'acme' must appear in second call even with cold discoverer")
}

func TestDiscoveryVariants_Distinct(t *testing.T) {
	for _, platform := range []string{
		engine.DiscoveryPlatformGreenhouse,
		engine.DiscoveryPlatformLever,
		engine.DiscoveryPlatformAshby,
	} {
		variants := discoveryVariants(platform, "golang", "")
		require.GreaterOrEqual(t, len(variants), 2, "platform %s needs >=2 variants", platform)

		seen := make(map[string]bool)
		for _, v := range variants {
			assert.False(t, seen[v], "platform %s has duplicate variant %q", platform, v)
			seen[v] = true
		}
	}
}
