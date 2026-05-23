package enrich_test

// Additional tests for PR #19 code-quality review findings.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/anatolykoptev/go_job/internal/hunt/enrich"
	"github.com/stretchr/testify/assert"
)

// TestEnrichBountyStatusGitHub_FetcherError_DoesNotUpdateChecked verifies that when
// FetchIssueInfoBatch returns an error, UpdateStatus is NOT called for any entry.
// MAJOR #1: prevents tight-loop where tokenless fetcher errors would cause infinite
// re-enrich of the same rows (last_checked_at never bumped on error).
func TestEnrichBountyStatusGitHub_FetcherError_DoesNotUpdateChecked(t *testing.T) {
	store := &mockStore{}
	fetcher := &mockFetcher{err: errors.New("github token unavailable")}
	entries := []hunt.Bounty{
		{ID: 10, URL: "https://github.com/org/repo/issues/10", Status: hunt.StatusOpen},
		{ID: 11, URL: "https://github.com/org/repo/issues/11", Status: hunt.StatusOpen},
	}
	enrich.EnrichBountyStatusGitHub(context.Background(), store, fetcher, entries, time.Hour)
	assert.Empty(t, store.updates,
		"UpdateStatus must NOT be called when fetcher returns error — last_checked_at must not be bumped")
}

// countingFetcher counts concurrent calls and records the peak.
type countingFetcher struct {
	mu      sync.Mutex
	current int
	peak    int32
}

func (c *countingFetcher) FetchIssueInfoBatch(ctx context.Context, urls []string) (map[string]enrich.GithubIssueInfo, error) {
	c.mu.Lock()
	c.current++
	if int32(c.current) > atomic.LoadInt32(&c.peak) {
		atomic.StoreInt32(&c.peak, int32(c.current))
	}
	c.mu.Unlock()

	time.Sleep(20 * time.Millisecond) // hold slot long enough to observe concurrency

	c.mu.Lock()
	c.current--
	c.mu.Unlock()

	// Return empty (all open, no updates needed).
	return map[string]enrich.GithubIssueInfo{}, nil
}

// TestEnrichBountyStatusGitHub_RespectsSemaphore verifies that concurrent enrich calls
// are bounded. When 6 goroutines call EnrichBountyStatusGitHub simultaneously with 1 URL
// each, the fetcher must not see more than enrichSemSize concurrent calls.
// MINOR #1: unbounded fan-out → N*M concurrent GitHub API calls under heavy list load.
func TestEnrichBountyStatusGitHub_RespectsSemaphore(t *testing.T) {
	const concurrency = 6

	cf := &countingFetcher{}
	store := &mockStore{}

	// Each goroutine calls EnrichBountyStatusGitHub with 1 unchecked GitHub bounty.
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			entries := []hunt.Bounty{
				{
					ID:     int64(100 + i),
					URL:    "https://github.com/org/repo/issues/" + string(rune('a'+i)),
					Status: hunt.StatusOpen,
				},
			}
			enrich.EnrichBountyStatusGitHub(context.Background(), store, cf, entries, time.Millisecond)
		}(i)
	}
	wg.Wait()

	// The semaphore limits concurrent enrich runs to enrichSemSize (4).
	// Peak must not exceed the semaphore size.
	assert.LessOrEqual(t, atomic.LoadInt32(&cf.peak), int32(4),
		"concurrent enrich runs must be bounded by semaphore (max 4)")
}
