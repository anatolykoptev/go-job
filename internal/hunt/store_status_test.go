package hunt_test

// Tests for Phase 3 status enrichment: store-level status lifecycle.
// All tests are DB-gated — skip if DATABASE_URL unset.

import (
	"context"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUpsert_PreservesClosedStatus verifies the core invariant:
// once a bounty is marked closed, a re-ingest with status="open" must NOT revert it.
// Ref: Phase 3 spec §3a — ON CONFLICT preserves non-open status.
func TestUpsert_PreservesClosedStatus(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateBounties(t, pool)

	b := hunt.Bounty{
		DedupHash: hunt.DedupHash("https://github.com/org/repo/issues/status-test-1"),
		Title:     "Status test",
		URL:       "https://github.com/org/repo/issues/status-test-1",
		Source:    "algora",
		Status:    hunt.StatusOpen,
	}

	id, _, err := s.UpsertBounty(ctx, b)
	require.NoError(t, err)

	// Mark as closed via UpdateStatus.
	now := time.Now()
	require.NoError(t, s.UpdateStatus(ctx, hunt.KindBounty, id, hunt.StatusClosed, &now))

	// Re-ingest with status=open (as if scraper sees it again).
	b.Status = hunt.StatusOpen
	_, _, err = s.UpsertBounty(ctx, b)
	require.NoError(t, err)

	got, err := s.GetBounty(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, hunt.StatusClosed, got.Status,
		"closed status must survive re-ingest of open record (ON CONFLICT must not revert)")
}

// TestUpdateStatus verifies UpdateStatus sets status, closed_at, last_checked_at.
func TestUpdateStatus(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateBounties(t, pool)

	b := hunt.Bounty{
		DedupHash: hunt.DedupHash("https://github.com/org/repo/issues/us-test-1"),
		Title:     "UpdateStatus test",
		URL:       "https://github.com/org/repo/issues/us-test-1",
		Source:    "algora",
		Status:    hunt.StatusOpen,
	}
	id, _, err := s.UpsertBounty(ctx, b)
	require.NoError(t, err)

	now := time.Now()
	require.NoError(t, s.UpdateStatus(ctx, hunt.KindBounty, id, hunt.StatusMerged, &now))

	got, err := s.GetBounty(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, hunt.StatusMerged, got.Status)
	require.NotNil(t, got.ClosedAt)
	require.NotNil(t, got.LastCheckedAt)
}

// TestUpdateStatusBatch verifies batch variant works across multiple IDs.
func TestUpdateStatusBatch(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateBounties(t, pool)

	now := time.Now()
	var ids []int64
	for i := 0; i < 3; i++ {
		u := "https://github.com/org/repo/issues/batch-test-" + string(rune('a'+i))
		b := hunt.Bounty{
			DedupHash: hunt.DedupHash(u),
			Title:     "Batch test " + string(rune('a'+i)),
			URL:       u,
			Source:    "algora",
			Status:    hunt.StatusOpen,
		}
		id, _, err := s.UpsertBounty(ctx, b)
		require.NoError(t, err)
		ids = append(ids, id)
	}

	updates := []hunt.StatusUpdate{
		{ID: ids[0], Status: hunt.StatusClosed, ClosedAt: &now},
		{ID: ids[1], Status: hunt.StatusMerged, ClosedAt: &now},
		{ID: ids[2], Status: hunt.StatusOpen, ClosedAt: nil},
	}
	require.NoError(t, s.UpdateStatusBatch(ctx, hunt.KindBounty, updates))

	got0, err := s.GetBounty(ctx, ids[0])
	require.NoError(t, err)
	assert.Equal(t, hunt.StatusClosed, got0.Status)

	got1, err := s.GetBounty(ctx, ids[1])
	require.NoError(t, err)
	assert.Equal(t, hunt.StatusMerged, got1.Status)

	got2, err := s.GetBounty(ctx, ids[2])
	require.NoError(t, err)
	assert.Equal(t, hunt.StatusOpen, got2.Status)
}

// TestListBounties_DefaultExcludesClosed verifies that ListBounties excludes
// closed/merged bounties by default (IncludeClosed=false).
func TestListBounties_DefaultExcludesClosed(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateBounties(t, pool)

	for i, status := range []string{hunt.StatusOpen, hunt.StatusOpen, hunt.StatusClosed, hunt.StatusMerged} {
		u := "https://github.com/org/repo/issues/filter-test-" + string(rune('a'+i))
		_, _, err := s.UpsertBounty(ctx, hunt.Bounty{
			DedupHash: hunt.DedupHash(u),
			Title:     "Filter test " + string(rune('a'+i)),
			URL:       u,
			Source:    "algora",
			Status:    status,
		})
		require.NoError(t, err)
	}

	result, err := s.ListBounties(ctx, hunt.BountyFilter{Limit: 10})
	require.NoError(t, err)
	assert.Len(t, result, 2, "default list must exclude closed/merged bounties")
	for _, b := range result {
		assert.Equal(t, hunt.StatusOpen, b.Status)
	}
}

// TestListBounties_IncludeClosedTrue verifies IncludeClosed=true returns all.
func TestListBounties_IncludeClosedTrue(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateBounties(t, pool)

	for i, status := range []string{hunt.StatusOpen, hunt.StatusClosed, hunt.StatusMerged} {
		u := "https://github.com/org/repo/issues/incl-test-" + string(rune('a'+i))
		_, _, err := s.UpsertBounty(ctx, hunt.Bounty{
			DedupHash: hunt.DedupHash(u),
			Title:     "Incl test " + string(rune('a'+i)),
			URL:       u,
			Source:    "algora",
			Status:    status,
		})
		require.NoError(t, err)
	}

	result, err := s.ListBounties(ctx, hunt.BountyFilter{Limit: 10, IncludeClosed: true})
	require.NoError(t, err)
	assert.Len(t, result, 3, "IncludeClosed=true must return all statuses")
}

// TestGetBountiesNeedingCheck verifies the method returns only open bounties
// whose last_checked_at is NULL or older than maxAge.
func TestGetBountiesNeedingCheck(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateBounties(t, pool)

	// Insert 3 bounties: unchecked, recently-checked, stale-checked.
	urls := []string{
		"https://github.com/org/repo/issues/check-test-a", // unchecked
		"https://github.com/org/repo/issues/check-test-b", // recently checked
		"https://github.com/org/repo/issues/check-test-c", // stale checked
	}
	ids := make([]int64, 3)
	for i, u := range urls {
		id, _, err := s.UpsertBounty(ctx, hunt.Bounty{
			DedupHash: hunt.DedupHash(u),
			Title:     "Check test " + string(rune('a'+i)),
			URL:       u,
			Source:    "algora",
			Status:    hunt.StatusOpen,
		})
		require.NoError(t, err)
		ids[i] = id
	}

	// Mark b as recently checked (< 1h ago).
	recent := time.Now().Add(-30 * time.Minute)
	_, err := pool.Exec(ctx, "UPDATE hunt_bounties SET last_checked_at=$1 WHERE id=$2", recent, ids[1])
	require.NoError(t, err)

	// Mark c as stale (> 2h ago).
	stale := time.Now().Add(-3 * time.Hour)
	_, err = pool.Exec(ctx, "UPDATE hunt_bounties SET last_checked_at=$1 WHERE id=$2", stale, ids[2])
	require.NoError(t, err)

	// With maxAge=1h: should return a (null) and c (stale), NOT b (recent).
	due, err := s.GetBountiesNeedingCheck(ctx, 1*time.Hour, 100)
	require.NoError(t, err)

	dueIDs := make(map[int64]bool)
	for _, b := range due {
		dueIDs[b.ID] = true
	}
	assert.True(t, dueIDs[ids[0]], "unchecked bounty must be in check-due list")
	assert.False(t, dueIDs[ids[1]], "recently-checked bounty must NOT be in check-due list")
	assert.True(t, dueIDs[ids[2]], "stale-checked bounty must be in check-due list")
}
