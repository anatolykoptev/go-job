package adminui

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/anatolykoptev/go-panel/components"
	"github.com/anatolykoptev/go-panel/resource"
	"github.com/anatolykoptev/go-panel/shell"
	"github.com/anatolykoptev/go_job/internal/hunt"
)

// navIDDashboard is the sidebar nav ID for the hunt dashboard page.
const navIDDashboard = "dashboard"

// dashboardStore is the minimal interface the dashboard handler requires.
// Defined at consumer (adminui) per Go convention; *hunt.Store satisfies it.
type dashboardStore interface {
	CountOpenJobs(ctx context.Context) int
	CountScored(ctx context.Context) int
	CountShortlist(ctx context.Context, user string, stages []string) int
	CountBySource(ctx context.Context) []hunt.SourceCount
}

// cachedSources builds a TTL-cached closure for CountBySource.
// The returned slice is safe to read concurrently; callers must not mutate it.
// Mirrors shell.CachedBadge but for []hunt.SourceCount instead of string.
func cachedSources(ttl time.Duration, fn func(context.Context) []hunt.SourceCount) func(context.Context) []hunt.SourceCount {
	var (
		mu      sync.Mutex
		last    []hunt.SourceCount
		expires time.Time
	)
	return func(ctx context.Context) []hunt.SourceCount {
		mu.Lock()
		defer mu.Unlock()
		if time.Now().Before(expires) {
			return last
		}
		last = fn(ctx)
		expires = time.Now().Add(ttl)
		return last
	}
}

// dashboardHandler returns the http.HandlerFunc for GET /admin/dashboard.
//
// All shell.CachedBadge (and cachedSources) closures are constructed ONCE at
// handler-construction scope — NOT inside the per-request closure. A fresh
// closure per request misses the cache and fires N live COUNT(*) per render
// (security HIGH finding F3). The second request fires 0 COUNT queries when
// the TTL has not expired.
func dashboardHandler(p *resource.Panel, store dashboardStore, adminUser string) http.HandlerFunc {
	const cacheTTL = 30 * time.Second

	totalBadge := shell.CachedBadge(cacheTTL, func(ctx context.Context) string {
		return strconv.Itoa(store.CountOpenJobs(ctx))
	})
	scoredBadge := shell.CachedBadge(cacheTTL, func(ctx context.Context) string {
		return strconv.Itoa(store.CountScored(ctx))
	})
	shortlistBadge := shell.CachedBadge(cacheTTL, func(ctx context.Context) string {
		return strconv.Itoa(store.CountShortlist(ctx, adminUser, shortlistActiveStages))
	})
	sourcesFunc := cachedSources(cacheTTL, store.CountBySource)

	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		srcs := sourcesFunc(ctx)
		sparkNums := make([]int, len(srcs))
		for i, s := range srcs {
			sparkNums[i] = s.N
		}

		grid := components.Grid(
			components.StatCardView(components.StatCard{Label: "Total", Value: totalBadge(ctx)}),
			components.StatCardView(components.StatCard{Label: "Scored", Value: scoredBadge(ctx)}),
			components.StatCardView(components.StatCard{Label: "Shortlist", Value: shortlistBadge(ctx)}),
			components.StatCardView(components.StatCard{Label: "Sources", Value: strconv.Itoa(len(srcs)), Spark: sparkNums}),
		)

		_ = p.RenderPage(w, r, "Hunt Dashboard", navIDDashboard, grid)
	}
}
