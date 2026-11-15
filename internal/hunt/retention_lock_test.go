package hunt_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// eventually polls a boolean SQL probe (up to 3s).
func eventually(t *testing.T, pool *pgxpool.Pool, probe string) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var ok bool
		require.NoError(t, pool.QueryRow(context.Background(), probe).Scan(&ok))
		if ok {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// TestRetentionLock_WriterInFlightBlocksPrune: a rating/pin writer is in
// flight (holding the shared retention lock, parked on another transaction's
// uncommitted unique key) when a prune batch starts. The batch must wait for
// the writer, and once the rating or pin has committed the job survives.
// Mutation: remove the shared lock from the writer (execRetentionShared /
// ToggleShortlistStar) and the batch no longer waits, deletes the job from its
// stale snapshot, and this test goes RED.
func TestRetentionLock_WriterInFlightBlocksPrune(t *testing.T) {
	cases := []struct {
		name    string
		parked  string // statement the writer is parked on
		hold    string // tx0: an uncommitted row with the writer's unique key
		holdArg func(jobID int64, acc *hunt.AccountStore) []any
		write   func(ctx context.Context, s *hunt.Store, acc *hunt.AccountStore, jobID int64) error
	}{
		{
			name:   "rating",
			parked: "%INSERT INTO hunt_ratings%",
			hold: `INSERT INTO hunt_ratings (entry_kind, entry_id, account_id, triage)
			       VALUES ('job', $1, $2, 'saved')`,
			holdArg: func(id int64, acc *hunt.AccountStore) []any { return []any{id, acc.AccountID()} },
			write: func(ctx context.Context, _ *hunt.Store, acc *hunt.AccountStore, id int64) error {
				return acc.SetTriage(ctx, hunt.KindJob, id, "interesting")
			},
		},
		{
			name:   "pin",
			parked: "%hunt_retention_pins%",
			hold: `INSERT INTO hunt_retention_pins (entry_kind, entry_id, reason)
			       VALUES ('job', $1, 'legacy_company')`,
			holdArg: func(id int64, _ *hunt.AccountStore) []any { return []any{id} },
			write: func(ctx context.Context, s *hunt.Store, _ *hunt.AccountStore, id int64) error {
				return s.PinJob(ctx, id, hunt.PinArtifact)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, acc := artifactFixture(t)
			pool := s.Pool()
			ctx := context.Background()
			id := staleUnratedJob(t, s, "lock-"+tc.name, "Acme", "Eng")

			tx0, err := pool.Begin(ctx)
			require.NoError(t, err)
			defer func() { _ = tx0.Rollback(ctx) }()
			_, err = tx0.Exec(ctx, tc.hold, tc.holdArg(id, acc)...)
			require.NoError(t, err)

			var wg sync.WaitGroup
			var writeErr error
			var pruned hunt.PruneResult
			var pruneErr error
			wg.Add(2)
			go func() { defer wg.Done(); writeErr = tc.write(ctx, s, acc, id) }()
			require.True(t, eventually(t, pool, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
				WHERE datname = current_database() AND wait_event_type = 'Lock'
				  AND query LIKE '`+tc.parked+`')`), "writer must be in flight (parked on tx0)")

			go func() { defer wg.Done(); pruned, pruneErr = s.PruneStale(ctx, 90) }()
			waited := eventually(t, pool, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND NOT granted)`)

			require.NoError(t, tx0.Commit(ctx)) // the rating/pin becomes visible
			wg.Wait()
			require.NoError(t, writeErr)
			require.NoError(t, pruneErr)

			assert.True(t, waited, "a prune batch must wait for an in-flight rating/pin writer")
			assert.True(t, rowExists(t, pool, "hunt_jobs", id), "job rated/pinned while the batch ran must survive")
			assert.Zero(t, pruned.Jobs)
		})
	}
}

// A candidate refreshed by an upsert while the DELETE waits on its row lock
// must survive: the outer last_seen_at re-check is evaluated against the
// updated row version. Mutation: drop `AND d.last_seen_at < ...` from
// pruneSQLTemplate and this goes RED.
func TestPruneStale_RefreshedWhileRowLockedSurvives(t *testing.T) {
	s, _ := artifactFixture(t)
	pool := s.Pool()
	ctx := context.Background()
	id := staleUnratedJob(t, s, "refresh", "Acme", "Eng")

	tx1, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx1.Rollback(ctx) }()
	_, err = tx1.Exec(ctx, `UPDATE hunt_jobs SET last_seen_at = NOW() WHERE id = $1`, id) // what an upsert does
	require.NoError(t, err)

	done := make(chan hunt.PruneResult, 1)
	go func() { r, _ := s.PruneStale(ctx, 90); done <- r }()
	require.True(t, eventually(t, pool, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
		WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE '%DELETE FROM hunt_jobs%')`),
		"the prune DELETE must be waiting on the row lock")
	require.NoError(t, tx1.Commit(ctx))

	res := <-done
	assert.Zero(t, res.Jobs, "refreshed posting must not be deleted")
	assert.True(t, rowExists(t, pool, "hunt_jobs", id))
}
