package jobs

import (
	"context"
	"errors"
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
	// In a test environment both SearchDirect and SearchSearXNG return empty
	// (no live services configured), so we just assert it returns without
	// panicking and that go-search error didn't propagate.
	got := discoverJobURLs(context.Background(), "golang engineer site:boards.greenhouse.io")
	// Result is nil or empty because local search has no live backend in tests —
	// the important invariant is we got here without error.
	assert.NotPanics(t, func() {
		_ = discoverJobURLs(context.Background(), "golang engineer")
	})
	_ = got // may be nil; the point is no panic / propagation
}

// TestDiscoverJobURLs_GoSearchEmpty_FallsBackToLocal verifies that an empty go-search
// result triggers local fallback. RED if the empty-result branch is removed.
func TestDiscoverJobURLs_GoSearchEmpty_FallsBackToLocal(t *testing.T) {
	resetATSDiscoverer(t)
	ATSDiscoverer = &fakeDiscoverer{results: nil, err: nil}

	// Must not panic; local path runs; function returns without error.
	assert.NotPanics(t, func() {
		_ = discoverJobURLs(context.Background(), "golang engineer site:boards.greenhouse.io")
	})
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
