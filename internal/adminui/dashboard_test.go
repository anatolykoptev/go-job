package adminui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/anatolykoptev/go-panel/auth"
	"github.com/anatolykoptev/go-panel/resource"
	"github.com/anatolykoptev/go_job/internal/hunt"
)

// countingDashStore is a dashboardStore stub that counts every call to a
// COUNT-issuing method. Used by the F3 cache-hit fitness test.
type countingDashStore struct {
	queries atomic.Int64
}

func (s *countingDashStore) CountOpenJobs(_ context.Context) int {
	s.queries.Add(1)
	return 10
}

func (s *countingDashStore) CountScored(_ context.Context) int {
	s.queries.Add(1)
	return 7
}

func (s *countingDashStore) CountShortlist(_ context.Context, _ string, _ []string) int {
	s.queries.Add(1)
	return 3
}

func (s *countingDashStore) CountBySource(_ context.Context) []hunt.SourceCount {
	s.queries.Add(1)
	return []hunt.SourceCount{
		{Source: "linkedin", N: 5},
		{Source: "indeed", N: 3},
		{Source: "himalayas", N: 2},
	}
}

// newDashTestPanel builds a minimal *resource.Panel for dashboard handler tests.
// Uses a real HMACAuth so resource.New does not panic.
func newDashTestPanel() *resource.Panel {
	a := auth.NewHMACAuth(auth.HMACConfig{
		Username: "admin",
		Password: "secret",
		HMACKey:  []byte("test-hmac-key-32-bytes-long-here"),
		BasePath: "/admin",
		Secure:   false,
	})
	return resource.New(resource.Config{
		Title:    "go-job test",
		BasePath: "/admin",
		Auth:     a,
	})
}

// TestDashboardHandler_FourStatCards renders the dashboard page once and asserts
// that the HTML contains exactly four stat-card elements (Total, Scored,
// Shortlist, Sources).
//
// RED-on-revert: removing any of the four StatCardView calls in dashboardHandler
// reduces the count of .stat-card divs and the assertion fails.
func TestDashboardHandler_FourStatCards(t *testing.T) {
	p := newDashTestPanel()
	store := &countingDashStore{}
	h := dashboardHandler(p, store, "admin")

	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/admin/dashboard", nil)
	h(w, r)

	body := w.Body.String()
	count := strings.Count(body, `class="stat-card"`)
	if count != 4 {
		t.Fatalf("expected 4 stat-card elements, got %d\nHTML (truncated):\n%s",
			count, body[:min(len(body), 3000)])
	}

	// Each label must appear.
	for _, label := range []string{"Total", "Scored", "Shortlist", "Sources"} {
		if !strings.Contains(body, label) {
			t.Errorf("stat-card label %q not found in HTML", label)
		}
	}
}

// TestDashboardHandler_CacheHit_ZeroCountsOnSecondRender renders the dashboard
// handler twice and asserts that the second render fires 0 COUNT queries (all
// four cached closures are warm after the first render).
//
// RED-on-revert: moving any CachedBadge/cachedSources construction inside the
// per-request handler body creates a fresh closure on every request; the second
// render fires 4 COUNT queries and this assertion fails.
func TestDashboardHandler_CacheHit_ZeroCountsOnSecondRender(t *testing.T) {
	p := newDashTestPanel()
	store := &countingDashStore{}
	h := dashboardHandler(p, store, "admin")

	// First request: warm the cache.
	r1 := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/admin/dashboard", nil)
	h(httptest.NewRecorder(), r1)

	// Reset query counter after warm-up.
	store.queries.Store(0)

	// Second request: must fire 0 COUNT queries.
	r2 := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/admin/dashboard", nil)
	h(httptest.NewRecorder(), r2)

	if got := store.queries.Load(); got != 0 {
		t.Fatalf("second render fired %d COUNT queries, want 0 (cache must be warm)", got)
	}
}
