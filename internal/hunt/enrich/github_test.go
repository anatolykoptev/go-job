package enrich_test

import (
	"context"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/anatolykoptev/go_job/internal/hunt/enrich"
	"github.com/stretchr/testify/assert"
)

// mockFetcher implements GithubIssueFetcher for unit tests.
type mockFetcher struct {
	results map[string]enrich.GithubIssueInfo
	err     error
}

func (m *mockFetcher) FetchIssueInfoBatch(ctx context.Context, urls []string) (map[string]enrich.GithubIssueInfo, error) {
	if m.err != nil {
		return nil, m.err
	}
	out := make(map[string]enrich.GithubIssueInfo)
	for _, u := range urls {
		if info, ok := m.results[u]; ok {
			out[u] = info
		}
	}
	return out, nil
}

// mockStore implements just UpdateStatus.
type mockStore struct {
	updates []statusCall
}

type statusCall struct {
	kind     string
	id       int64
	status   string
	closedAt *time.Time
}

func (m *mockStore) UpdateStatus(ctx context.Context, kind string, id int64, status string, closedAt *time.Time) error {
	m.updates = append(m.updates, statusCall{kind: kind, id: id, status: status, closedAt: closedAt})
	return nil
}

// TestEnrichBountyStatusGitHub_SkipsNonGitHub verifies that bounties with non-github
// URLs are not fetched or updated.
func TestEnrichBountyStatusGitHub_SkipsNonGitHub(t *testing.T) {
	store := &mockStore{}
	fetcher := &mockFetcher{}
	entries := []hunt.Bounty{
		{ID: 1, URL: "https://algora.io/some/path", Status: hunt.StatusOpen},
		{ID: 2, URL: "https://opire.dev/bounty/123", Status: hunt.StatusOpen},
	}
	enrich.EnrichBountyStatusGitHub(context.Background(), store, fetcher, entries, time.Hour)
	assert.Empty(t, store.updates, "non-github URLs must not trigger UpdateStatus")
	assert.Empty(t, fetcher.results, "non-github URLs must not hit fetcher")
}

// TestEnrichBountyStatusGitHub_SkipsRecent verifies that bounties checked
// within maxAge are not re-fetched.
func TestEnrichBountyStatusGitHub_SkipsRecent(t *testing.T) {
	store := &mockStore{}
	recent := time.Now().Add(-10 * time.Minute)
	fetcher := &mockFetcher{}
	entries := []hunt.Bounty{
		{
			ID:            1,
			URL:           "https://github.com/org/repo/issues/1",
			Status:        hunt.StatusOpen,
			LastCheckedAt: &recent,
		},
	}
	enrich.EnrichBountyStatusGitHub(context.Background(), store, fetcher, entries, time.Hour)
	assert.Empty(t, store.updates, "recently-checked bounty must not trigger UpdateStatus")
}

// TestEnrichBountyStatusGitHub_UpdatesClosed verifies that when GitHub returns
// state=closed, UpdateStatus is called with StatusClosed and a non-nil closedAt.
func TestEnrichBountyStatusGitHub_UpdatesClosed(t *testing.T) {
	store := &mockStore{}
	ghURL := "https://github.com/org/repo/issues/42"
	fetcher := &mockFetcher{
		results: map[string]enrich.GithubIssueInfo{
			ghURL: {State: "closed", Merged: false},
		},
	}
	entries := []hunt.Bounty{
		{ID: 7, URL: ghURL, Status: hunt.StatusOpen},
	}
	enrich.EnrichBountyStatusGitHub(context.Background(), store, fetcher, entries, time.Hour)

	require := assert.New(t)
	require.Len(store.updates, 1)
	require.Equal(hunt.KindBounty, store.updates[0].kind)
	require.Equal(int64(7), store.updates[0].id)
	require.Equal(hunt.StatusClosed, store.updates[0].status)
	require.NotNil(store.updates[0].closedAt)
}

// TestEnrichBountyStatusGitHub_UpdatesMerged verifies state=closed+Merged=true → StatusMerged.
func TestEnrichBountyStatusGitHub_UpdatesMerged(t *testing.T) {
	store := &mockStore{}
	ghURL := "https://github.com/org/repo/issues/99"
	fetcher := &mockFetcher{
		results: map[string]enrich.GithubIssueInfo{
			ghURL: {State: "closed", Merged: true},
		},
	}
	entries := []hunt.Bounty{
		{ID: 9, URL: ghURL, Status: hunt.StatusOpen},
	}
	enrich.EnrichBountyStatusGitHub(context.Background(), store, fetcher, entries, time.Hour)

	assert.Len(t, store.updates, 1)
	assert.Equal(t, hunt.StatusMerged, store.updates[0].status)
}

// TestEnrichBountyStatusGitHub_SkipsAlreadyClosed verifies that entries
// with Status != open are skipped without fetching.
func TestEnrichBountyStatusGitHub_SkipsAlreadyClosed(t *testing.T) {
	store := &mockStore{}
	fetcher := &mockFetcher{}
	entries := []hunt.Bounty{
		{ID: 3, URL: "https://github.com/org/repo/issues/3", Status: hunt.StatusClosed},
		{ID: 4, URL: "https://github.com/org/repo/issues/4", Status: hunt.StatusMerged},
	}
	enrich.EnrichBountyStatusGitHub(context.Background(), store, fetcher, entries, time.Hour)
	assert.Empty(t, store.updates, "already-closed entries must be skipped")
}
