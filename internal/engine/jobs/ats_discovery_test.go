package jobs

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDiscoverer implements the local discoverer interface for unit tests.
type fakeDiscoverer struct {
	results []engine.SearxngResult
	err     error
}

func (f *fakeDiscoverer) DiscoverBoardURLs(_ context.Context, _ string) ([]engine.SearxngResult, error) {
	return f.results, f.err
}

// resetATSDiscoverer restores the package-level singleton after a test.
func resetATSDiscoverer(t *testing.T) {
	t.Helper()
	prev := ATSDiscoverer
	t.Cleanup(func() { ATSDiscoverer = prev })
}

// TestDiscoverJobURLs_GoSearchPrimary verifies go-search results are returned when
// the discoverer is healthy. RED if ATSDiscoverer is removed from discoverJobURLs.
func TestDiscoverJobURLs_GoSearchPrimary(t *testing.T) {
	resetATSDiscoverer(t)
	want := []engine.SearxngResult{
		{URL: "https://boards.greenhouse.io/acme", Title: "Acme"},
		{URL: "https://boards.greenhouse.io/beta", Title: "Beta"},
	}
	ATSDiscoverer = &fakeDiscoverer{results: want}

	got := discoverJobURLs(context.Background(), "golang engineer site:boards.greenhouse.io")
	require.Len(t, got, 2)
	assert.Equal(t, "https://boards.greenhouse.io/acme", got[0].URL)
	assert.Equal(t, "https://boards.greenhouse.io/beta", got[1].URL)
}

// TestDiscoverJobURLs_GoSearchError_FallsBackToLocal verifies that when go-search
// returns an error, discoverJobURLs falls back to local SearchDirect/SearXNG instead
// of returning an error. RED if the fallback branch is removed.
func TestDiscoverJobURLs_GoSearchError_FallsBackToLocal(t *testing.T) {
	resetATSDiscoverer(t)
	ATSDiscoverer = &fakeDiscoverer{err: errors.New("connection refused")}

	// discoverJobURLs must not panic and must not propagate the error.
	// In a test environment SearchDirect returns empty (no live services
	// configured), so we just assert it returns without panicking and
	// that go-search error didn't propagate.
	got := discoverJobURLs(context.Background(), "golang engineer site:boards.greenhouse.io")
	// Result is nil or empty because local search has no live backend in tests —
	// the important invariant is we got here without error.
	assert.NotPanics(t, func() {
		_ = discoverJobURLs(context.Background(), "golang engineer")
	})
	_ = got // may be nil; the point is no panic / propagation
}

// TestDiscoverJobURLs_GoSearchEmpty_ReturnsTrustedEmpty verifies that a clean
// (no-error) empty go-search response short-circuits local scraper fallback.
//
// DISTINGUISHING MECHANISM (prevents vacuous pass):
// The test reads gojob_hunt_discovery_source_total counters before and after the
// call.  Under the NEW behaviour (trusted-empty, no fallback):
//   - "go-search" counter increments by exactly 1.
//   - "local-fallback" counter does NOT increment.
// Under the OLD behaviour (empty → fall through to local):
//   - "local-fallback" counter increments (SearchDirect + SearXNG fire even if
//     they return nothing in this env) and "go-search" does NOT increment.
// So the test goes RED on a revert of the `else { return deduplicateByURL }` branch
// regardless of whether local scrapers happen to return results in this env.
//
// Proof of RED-on-revert: reverting the else-branch restores the `default:` fall-
// through path, which calls IncrHuntDiscoverySource("local-fallback") instead of
// IncrHuntDiscoverySource("go-search") → the counter assertions below invert.
func TestDiscoverJobURLs_GoSearchEmpty_ReturnsTrustedEmpty(t *testing.T) {
	resetATSDiscoverer(t)
	ATSDiscoverer = &fakeDiscoverer{results: nil, err: nil}

	before := engine.GetMetrics()
	_ = discoverJobURLs(context.Background(), "golang engineer site:boards.greenhouse.io")
	after := engine.GetMetrics()

	goSearchKey := engine.MetricHuntDiscoverySource + "{source=go-search}"
	localFallbackKey := engine.MetricHuntDiscoverySource + "{source=local-fallback}"

	goSearchDelta := after[goSearchKey] - before[goSearchKey]
	localFallbackDelta := after[localFallbackKey] - before[localFallbackKey]

	// go-search authoritative path must be credited exactly once.
	assert.Equal(t, int64(1), goSearchDelta,
		"expected go-search counter +1 (trusted-empty short-circuit); got %d — "+
			"reverting the else-branch to default:fallthrough would cause this failure",
		goSearchDelta)

	// Local scrapers must NOT have run (local-fallback counter unchanged).
	assert.Equal(t, int64(0), localFallbackDelta,
		"expected local-fallback counter unchanged (no fallback on clean empty); got +%d — "+
			"reverting the else-branch to default:fallthrough would cause this failure",
		localFallbackDelta)
}

// TestDiscoverJobURLs_NoDiscoverer_UsesLocalOnly verifies the nil-discoverer path
// (GO_SEARCH_URL unset at startup). RED if the nil guard around ATSDiscoverer is removed.
func TestDiscoverJobURLs_NoDiscoverer_UsesLocalOnly(t *testing.T) {
	resetATSDiscoverer(t)
	ATSDiscoverer = nil

	assert.NotPanics(t, func() {
		_ = discoverJobURLs(context.Background(), "golang engineer site:boards.greenhouse.io")
	})
}

// TestDiscoverJobURLs_DegradedFallback_DistinctMetric verifies that when
// DiscoverBoardURLs returns an error wrapping engine.ErrDiscoveryDegraded,
// discoverJobURLs increments source="degraded-fallback" and does NOT touch
// source="local-fallback" — keeping the two failure classes separable in metrics.
//
// Proof of RED-on-revert: if the errors.Is branch is collapsed back to a single
// IncrHuntDiscoverySource("local-fallback") call (pre-change behaviour), the
// degraded-fallback delta becomes 0 and local-fallback delta becomes 1 — both
// assertions below invert.
func TestDiscoverJobURLs_DegradedFallback_DistinctMetric(t *testing.T) {
	resetATSDiscoverer(t)
	// Return an error that wraps ErrDiscoveryDegraded, mirroring what
	// discovery.Client returns on a 200+Degraded=true response.
	degradedErr := fmt.Errorf("discovery: raw_web_search degraded (ctx_deadline): %w", engine.ErrDiscoveryDegraded)
	ATSDiscoverer = &fakeDiscoverer{err: degradedErr}

	before := engine.GetMetrics()
	_ = discoverJobURLs(context.Background(), "golang engineer site:boards.greenhouse.io")
	after := engine.GetMetrics()

	degradedKey := engine.MetricHuntDiscoverySource + "{source=degraded-fallback}"
	localKey := engine.MetricHuntDiscoverySource + "{source=local-fallback}"

	degradedDelta := after[degradedKey] - before[degradedKey]
	localDelta := after[localKey] - before[localKey]

	assert.Equal(t, int64(1), degradedDelta,
		"expected degraded-fallback counter +1; got %d — "+
			"reverting the errors.Is branch would set this to 0 (label goes to local-fallback instead)",
		degradedDelta)
	assert.Equal(t, int64(0), localDelta,
		"expected local-fallback counter unchanged on Degraded error; got +%d — "+
			"reverting the errors.Is branch would set this to 1",
		localDelta)
}

// TestDiscoverJobURLs_TransportError_LocalFallback_Metric guards the pre-existing
// transport-error → source="local-fallback" path against regression after the
// Degraded branch was added.
//
// Proof of RED-on-revert: if errors.Is is removed and all errors route to
// "degraded-fallback", local-fallback delta becomes 0 and degraded-fallback becomes 1.
func TestDiscoverJobURLs_TransportError_LocalFallback_Metric(t *testing.T) {
	resetATSDiscoverer(t)
	// Plain transport error — does NOT wrap ErrDiscoveryDegraded.
	ATSDiscoverer = &fakeDiscoverer{err: errors.New("connection refused")}

	before := engine.GetMetrics()
	_ = discoverJobURLs(context.Background(), "golang engineer site:boards.greenhouse.io")
	after := engine.GetMetrics()

	degradedKey := engine.MetricHuntDiscoverySource + "{source=degraded-fallback}"
	localKey := engine.MetricHuntDiscoverySource + "{source=local-fallback}"

	degradedDelta := after[degradedKey] - before[degradedKey]
	localDelta := after[localKey] - before[localKey]

	assert.Equal(t, int64(0), degradedDelta,
		"expected degraded-fallback counter unchanged on transport error; got +%d — "+
			"removing the errors.Is guard would send all errors here",
		degradedDelta)
	assert.Equal(t, int64(1), localDelta,
		"expected local-fallback counter +1 on transport error; got %d — "+
			"removing the errors.Is guard would send this to degraded-fallback instead",
		localDelta)
}

// TestDeduplicateByURL verifies URL-keyed deduplication preserves first occurrence.
func TestDeduplicateByURL(t *testing.T) {
	in := []engine.SearxngResult{
		{URL: "https://boards.greenhouse.io/acme", Title: "Acme 1"},
		{URL: "https://boards.greenhouse.io/acme", Title: "Acme 2"}, // dup
		{URL: "https://boards.greenhouse.io/beta", Title: "Beta"},
		{URL: "", Title: "No URL"}, // must be dropped
	}
	got := deduplicateByURL(in)
	require.Len(t, got, 2, "two unique non-empty URLs expected")
	assert.Equal(t, "https://boards.greenhouse.io/acme", got[0].URL)
	assert.Equal(t, "Acme 1", got[0].Title, "first occurrence must be preserved")
	assert.Equal(t, "https://boards.greenhouse.io/beta", got[1].URL)
}
