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
	"github.com/google/uuid"
)

// navIDDashboard is the sidebar nav ID for the hunt dashboard page.
const navIDDashboard = "dashboard"

// dashboardStore is the minimal interface the dashboard handler requires.
// Defined at consumer (adminui) per Go convention; *hunt.Store satisfies it.
// ForAccount binds the per-account score counter (nil store → nil facade).
type dashboardStore interface {
	CountOpenJobs(ctx context.Context) int
	CountShortlist(ctx context.Context, user string, triageValues, stageValues []string) int
	CountBySource(ctx context.Context) []hunt.SourceCount
	ForAccount(aid uuid.UUID) *hunt.AccountStore
}

// perAccountBadge is CachedBadge keyed by account: an unkeyed TTL cache would
// leak account A's scored count into account B's dashboard for up to `ttl`.
func perAccountBadge(ttl time.Duration, fn func(ctx context.Context, aid uuid.UUID) string) func(ctx context.Context, aid uuid.UUID) string {
	type entry struct {
		val     string
		expires time.Time
	}
	var mu sync.Mutex
	cache := map[uuid.UUID]entry{}
	return func(ctx context.Context, aid uuid.UUID) string {
		mu.Lock()
		defer mu.Unlock()
		if e, ok := cache[aid]; ok && time.Now().Before(e.expires) {
			return e.val
		}
		v := fn(ctx, aid)
		cache[aid] = entry{v, time.Now().Add(ttl)}
		return v
	}
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
func dashboardHandler(p *resource.Panel, store dashboardStore, adminUser string, acctOf accountResolver) http.HandlerFunc {
	const cacheTTL = 30 * time.Second

	totalBadge := shell.CachedBadge(cacheTTL, func(ctx context.Context) string {
		return strconv.Itoa(store.CountOpenJobs(ctx))
	})
	// Scored count is per-account (account_job_scores) — cached per account id
	// so one account's number can never render on another's dashboard.
	scoredBadge := perAccountBadge(cacheTTL, func(ctx context.Context, aid uuid.UUID) string {
		as := store.ForAccount(aid)
		if as == nil {
			return "0"
		}
		return strconv.Itoa(as.CountScored(ctx))
	})
	shortlistBadge := shell.CachedBadge(cacheTTL, func(ctx context.Context) string {
		return strconv.Itoa(store.CountShortlist(ctx, adminUser, shortlistTriageValues, shortlistPipelineValues))
	})
	sourcesFunc := cachedSources(cacheTTL, store.CountBySource)

	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		aid, _ := acctOf(ctx) // miss → uuid.Nil → zero score rows match

		srcs := sourcesFunc(ctx)
		sparkNums := make([]int, len(srcs))
		for i, s := range srcs {
			sparkNums[i] = s.N
		}

		grid := components.Grid(
			components.StatCardView(components.StatCard{Label: "Total", Value: totalBadge(ctx)}),
			components.StatCardView(components.StatCard{Label: "Scored", Value: scoredBadge(ctx, aid)}),
			components.StatCardView(components.StatCard{Label: "Shortlist", Value: shortlistBadge(ctx)}),
			components.StatCardView(components.StatCard{Label: "Sources", Value: strconv.Itoa(len(srcs)), Spark: sparkNums}),
		)

		_ = p.RenderPage(w, r, "Hunt Dashboard", navIDDashboard, grid)
	}
}
