package hunt_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/google/uuid"
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

// pruneWaitingProbe is true while a prune batch of THIS database is parked on
// the retention lock: an ungranted EXCLUSIVE advisory request on retentionLockKey
// (0x68756e7472657431 = classid 0x68756e74, objid 0x72657431, objsubid 1) made
// by the prune's own statement. Shared requests (writers) and other keys or
// databases (parallel packages) never match. If the key is ever changed this
// probe stops matching and the test goes RED, not green.
const pruneWaitingProbe = `SELECT EXISTS (
	SELECT 1 FROM pg_locks l JOIN pg_stat_activity a USING (pid)
	WHERE l.locktype = 'advisory' AND NOT l.granted AND l.mode = 'ExclusiveLock'
	  AND l.classid = 1752526452 AND l.objid = 1919251505 AND l.objsubid = 1
	  AND a.datname = current_database()
	  AND a.query LIKE '%pg_advisory_xact_lock($1)%')`

// TestRetentionLock_WriterInFlightBlocksPrune: a writer that creates retention
// protection (a hunt_ratings row or a hunt_retention_pins row) is in flight,
// holding the shared retention lock and parked on another transaction's
// uncommitted unique key, when a prune batch starts. The batch must wait for
// the writer; once the rating or pin has committed the job survives.
// One sub-test per writer. Mutation: remove the shared lock from a writer
// (lockRetentionShared in ToggleShortlistStar, or execRetentionShared ->
// pool.Exec in Rate/RateExact/SetTriage/SetStage/PinJob/the backfill) and that
// writer's sub-test goes RED: the batch no longer waits.
func TestRetentionLock_WriterInFlightBlocksPrune(t *testing.T) {
	const holdRating = `INSERT INTO hunt_ratings (entry_kind, entry_id, account_id, triage)
		VALUES ('job', $1, $2, 'saved')`
	const holdPin = `INSERT INTO hunt_retention_pins (entry_kind, entry_id, reason)
		VALUES ('job', $1, 'legacy_company')`
	ratingArgs := func(id int64, acc *hunt.AccountStore) []any { return []any{id, acc.AccountID()} }
	pinArgs := func(id int64, _ *hunt.AccountStore) []any { return []any{id} }
	const parkedRating, parkedPin = "%INSERT INTO hunt_ratings%", "%INSERT INTO hunt_retention_pins%"

	cases := []struct {
		name    string
		parked  string // statement the writer is parked on
		hold    string // tx0: an uncommitted row with the writer's unique key
		holdArg func(jobID int64, acc *hunt.AccountStore) []any
		write   func(ctx context.Context, s *hunt.Store, acc *hunt.AccountStore, jobID int64) error
	}{
		{"Rate", parkedRating, holdRating, ratingArgs,
			func(ctx context.Context, _ *hunt.Store, acc *hunt.AccountStore, id int64) error {
				return acc.Rate(ctx, hunt.KindJob, id, "interesting", "", "")
			}},
		{"RateExact", parkedRating, holdRating, ratingArgs,
			func(ctx context.Context, _ *hunt.Store, acc *hunt.AccountStore, id int64) error {
				return acc.RateExact(ctx, hunt.KindJob, id, "interesting", "", "")
			}},
		{"SetTriage", parkedRating, holdRating, ratingArgs,
			func(ctx context.Context, _ *hunt.Store, acc *hunt.AccountStore, id int64) error {
				return acc.SetTriage(ctx, hunt.KindJob, id, "interesting")
			}},
		{"SetStage", parkedRating, holdRating, ratingArgs,
			func(ctx context.Context, _ *hunt.Store, acc *hunt.AccountStore, id int64) error {
				return acc.SetStage(ctx, hunt.KindJob, id, "applied")
			}},
		{"ToggleShortlistStar", parkedRating, holdRating, ratingArgs,
			func(ctx context.Context, _ *hunt.Store, acc *hunt.AccountStore, id int64) error {
				_, err := acc.ToggleShortlistStar(ctx, id, []string{"claimed", "applied", "interview", "offer"}, []string{"interesting", "saved"})
				return err
			}},
		{"PinJob", parkedPin, holdPin, pinArgs,
			func(ctx context.Context, s *hunt.Store, _ *hunt.AccountStore, id int64) error {
				return s.PinJob(ctx, id, hunt.PinArtifact)
			}},
		{"BackfillArtifactMarkers", parkedPin, holdPin, pinArgs,
			func(ctx context.Context, s *hunt.Store, _ *hunt.AccountStore, id int64) error {
				_, err := s.BackfillArtifactMarkers(ctx, fakeIndex{jobIDs: []int64{id}})
				return err
			}},
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
			var writeErr, pruneErr error
			var pruned hunt.PruneResult
			wg.Add(2)
			go func() { defer wg.Done(); writeErr = tc.write(ctx, s, acc, id) }()
			require.True(t, eventually(t, pool, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
				WHERE datname = current_database() AND wait_event_type = 'Lock'
				  AND query LIKE '`+tc.parked+`')`), "writer must be in flight (parked on tx0)")

			go func() { defer wg.Done(); pruned, pruneErr = s.PruneStale(ctx, 90) }()
			waited := eventually(t, pool, pruneWaitingProbe)

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

// fakeIndex is an ArtifactIndex with a fixed artifact job list and no legacy
// directories.
type fakeIndex struct{ jobIDs []int64 }

func (f fakeIndex) ArtifactJobRefs() ([]uuid.UUID, []int64, error) {
	return make([]uuid.UUID, len(f.jobIDs)), f.jobIDs, nil
}
func (fakeIndex) LegacyOwner() uuid.UUID                         { return uuid.Nil }
func (fakeIndex) LegacyMatcher() (func(string) bool, int, error) { return nil, 0, nil }

// A batch whose DELETE waits on a row lock must give up after lock_timeout and
// end the run quietly (no error), leaving the row; otherwise it would hold the
// exclusive retention lock, and block every rating/pin writer, indefinitely.
// Mutation: remove `SET LOCAL lock_timeout` from pruneBatch and the prune blocks
// until the test's context deadline, so PruneStale returns an error: RED.
func TestPruneStale_RowLockTimeoutEndsRunQuietly(t *testing.T) {
	s, _ := artifactFixture(t)
	pool := s.Pool()
	id := staleUnratedJob(t, s, "locktimeout", "Acme", "Eng")

	bg := context.Background()
	tx1, err := pool.Begin(bg)
	require.NoError(t, err)
	defer func() { _ = tx1.Rollback(bg) }()
	_, err = tx1.Exec(bg, `SELECT id FROM hunt_jobs WHERE id = $1 FOR UPDATE`, id)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(bg, 9*time.Second)
	defer cancel()
	start := time.Now()
	res, err := s.PruneStale(ctx, 90)
	require.NoError(t, err, "lock timeout must end the run quietly, not fail it (ctx err: %v)", ctx.Err())
	assert.Less(t, time.Since(start), 8*time.Second)
	assert.Zero(t, res.Jobs)

	require.NoError(t, tx1.Rollback(bg))
	assert.True(t, rowExists(t, pool, "hunt_jobs", id), "locked candidate must survive the abandoned batch")
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
