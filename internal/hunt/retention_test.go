package hunt_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// retentionFixture holds one old-unrated, one old-rated and one recent row per
// pruned table.
type retentionFixture struct {
	s   *hunt.Store
	acc *hunt.AccountStore
	// ids by kind: [0]=old unrated, [1]=old rated, [2]=recent
	jobs, freelance, security [3]int64
}

func ageRow(t *testing.T, pool *pgxpool.Pool, table string, id int64, days int) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		fmt.Sprintf(`UPDATE %s SET last_seen_at = NOW() - make_interval(days => $2::int) WHERE id = $1`, table),
		id, days)
	require.NoError(t, err)
}

func rowExists(t *testing.T, pool *pgxpool.Pool, table string, id int64) bool {
	t.Helper()
	var ok bool
	require.NoError(t, pool.QueryRow(context.Background(),
		fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE id = $1)`, table), id).Scan(&ok))
	return ok
}

func newRetentionFixture(t *testing.T) *retentionFixture {
	t.Helper()
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	acc := s.ForAccount(newScoreAccount(t, pool))
	truncateJobs(t, pool)
	truncateSecurity(t, pool)
	_, err := pool.Exec(ctx, "TRUNCATE hunt_freelance, hunt_ratings CASCADE")
	require.NoError(t, err)

	f := &retentionFixture{s: s, acc: acc}
	for i, name := range []string{"old-unrated", "old-rated", "recent"} {
		u := "https://retention.example/" + name
		jid, _, err := s.UpsertJob(ctx, hunt.Job{DedupHash: hunt.DedupHash("job" + u), Title: name, URL: u, Source: "ret"})
		require.NoError(t, err)
		fid, _, err := s.UpsertFreelance(ctx, hunt.Freelance{DedupHash: hunt.DedupHash("fl" + u), Title: name, URL: u, Platform: "p", Source: "ret"})
		require.NoError(t, err)
		sid, _, err := s.UpsertSecurity(ctx, hunt.Security{DedupHash: hunt.DedupHash("sec" + u), Name: name, URL: u, Platform: "p"})
		require.NoError(t, err)
		f.jobs[i], f.freelance[i], f.security[i] = jid, fid, sid
		if i < 2 { // old
			ageRow(t, pool, "hunt_jobs", jid, 200)
			ageRow(t, pool, "hunt_freelance", fid, 200)
			ageRow(t, pool, "hunt_security", sid, 200)
		}
	}
	// "acted on": a triage rating on each old-rated row.
	require.NoError(t, acc.SetTriage(ctx, hunt.KindJob, f.jobs[1], "saved"))
	require.NoError(t, acc.SetTriage(ctx, hunt.KindFreelance, f.freelance[1], "interesting"))
	require.NoError(t, acc.SetStage(ctx, hunt.KindSecurity, f.security[1], hunt.StageApplied))
	return f
}

func TestPruneStale_DeletesOldUnrated(t *testing.T) {
	f := newRetentionFixture(t)
	res, err := f.s.PruneStale(context.Background(), 90)
	require.NoError(t, err)

	pool := f.s.Pool()
	assert.False(t, rowExists(t, pool, "hunt_jobs", f.jobs[0]), "old unrated job must be pruned")
	assert.False(t, rowExists(t, pool, "hunt_freelance", f.freelance[0]), "old unrated freelance must be pruned")
	assert.False(t, rowExists(t, pool, "hunt_security", f.security[0]), "old unrated security must be pruned")
	assert.Equal(t, hunt.PruneResult{Jobs: 1, Freelance: 1, Security: 1}, res)
}

// TestPruneStale_KeepsRatedRows is the silent-failure guard: dropping the
// NOT EXISTS (hunt_ratings) clause in pruneSQLTemplate deletes rows a user
// triaged or applied to, and nothing errors. Mutation: remove that clause and
// this test must go RED.
func TestPruneStale_KeepsRatedRows(t *testing.T) {
	f := newRetentionFixture(t)
	_, err := f.s.PruneStale(context.Background(), 90)
	require.NoError(t, err)

	pool := f.s.Pool()
	assert.True(t, rowExists(t, pool, "hunt_jobs", f.jobs[1]), "rated job must be kept")
	assert.True(t, rowExists(t, pool, "hunt_freelance", f.freelance[1]), "rated freelance must be kept")
	assert.True(t, rowExists(t, pool, "hunt_security", f.security[1]), "rated security must be kept")
}

func TestPruneStale_KeepsRecentRows(t *testing.T) {
	f := newRetentionFixture(t)
	_, err := f.s.PruneStale(context.Background(), 90)
	require.NoError(t, err)

	pool := f.s.Pool()
	assert.True(t, rowExists(t, pool, "hunt_jobs", f.jobs[2]))
	assert.True(t, rowExists(t, pool, "hunt_freelance", f.freelance[2]))
	assert.True(t, rowExists(t, pool, "hunt_security", f.security[2]))
}

func TestPruneStale_ZeroDisables(t *testing.T) {
	f := newRetentionFixture(t)
	res, err := f.s.PruneStale(context.Background(), 0)
	require.NoError(t, err)
	assert.Equal(t, hunt.PruneResult{}, res)
	assert.True(t, rowExists(t, f.s.Pool(), "hunt_jobs", f.jobs[0]), "retention 0 must delete nothing")

	t.Setenv("HUNT_RETENTION_DAYS", "0")
	assert.Equal(t, 0, hunt.RetentionDays())
	t.Setenv("HUNT_RETENTION_DAYS", "3")
	assert.Equal(t, 7, hunt.RetentionDays(), "positive values below the floor are raised")
	t.Setenv("HUNT_RETENTION_DAYS", "")
	assert.Equal(t, hunt.DefaultRetentionDays, hunt.RetentionDays())
}

func TestPruneStale_BatchesUntilDrained(t *testing.T) {
	f := newRetentionFixture(t)
	pool := f.s.Pool()
	ctx := context.Background()
	for i := range 7 {
		u := fmt.Sprintf("https://retention.example/bulk/%d", i)
		id, _, err := f.s.UpsertJob(ctx, hunt.Job{DedupHash: hunt.DedupHash(u), Title: "bulk", URL: u, Source: "ret"})
		require.NoError(t, err)
		ageRow(t, pool, "hunt_jobs", id, 400)
	}
	res, err := f.s.PruneStaleBatch(ctx, 90, 3) // 8 stale rows, batch 3 -> 3 statements
	require.NoError(t, err)
	assert.Equal(t, int64(8), res.Jobs, "all stale rows go, across batches")
	var left int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM hunt_jobs WHERE last_seen_at < NOW() - interval '90 days'
		   AND id <> $1`, f.jobs[1]).Scan(&left))
	assert.Zero(t, left)
}

// TestPruneStale_CascadesScores: the prune must not strand account_job_scores
// rows (they have no user signal, and the FK removes them with the job).
func TestPruneStale_CascadesScores(t *testing.T) {
	f := newRetentionFixture(t)
	ctx := context.Background()
	pool := f.s.Pool()
	for _, id := range []int64{f.jobs[0], f.jobs[2]} {
		_, err := pool.Exec(ctx,
			`INSERT INTO account_job_scores (account_id, job_id, fit_score) VALUES ($1, $2, 50)`,
			f.acc.AccountID(), id)
		require.NoError(t, err)
	}
	_, err := f.s.PruneStale(ctx, 90)
	require.NoError(t, err)

	var n int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM account_job_scores WHERE job_id = $1`, f.jobs[0]).Scan(&n))
	assert.Zero(t, n, "pruned job's scores must go with it")
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM account_job_scores WHERE job_id = $1`, f.jobs[2]).Scan(&n))
	assert.Equal(t, 1, n, "kept job's score must stay")
}

// TestUpsertSecurity_UnchangedTickWritesNothing: a second identical ingest must
// not create a new tuple version (xmin/ctid stay put); a stale last_seen_at or
// a status change must still be written.
func TestUpsertSecurity_UnchangedTickWritesNothing(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateSecurity(t, pool)

	sec := hunt.Security{DedupHash: hunt.DedupHash("https://noop.example/p"), Name: "Noop", URL: "https://noop.example/p", Platform: "h1"}
	id, outcome, err := s.UpsertSecurity(ctx, sec)
	require.NoError(t, err)
	require.Equal(t, hunt.OutcomeCreated, outcome)

	version := func() (string, time.Time) {
		var v string
		var seen time.Time
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT xmin::text || '/' || ctid::text, last_seen_at FROM hunt_security WHERE id = $1`, id).Scan(&v, &seen))
		return v, seen
	}
	v1, seen1 := version()

	id2, outcome2, err := s.UpsertSecurity(ctx, sec)
	require.NoError(t, err)
	assert.Equal(t, id, id2, "unchanged tick still returns the row id")
	assert.Equal(t, hunt.OutcomeMerged, outcome2)
	v2, seen2 := version()
	assert.Equal(t, v1, v2, "identical second tick must write 0 rows")
	assert.True(t, seen1.Equal(seen2))

	// last_seen_at older than the refresh window is rewritten.
	_, err = pool.Exec(ctx, `UPDATE hunt_security SET last_seen_at = NOW() - interval '3 days' WHERE id = $1`, id)
	require.NoError(t, err)
	v3, _ := version()
	_, outcome3, err := s.UpsertSecurity(ctx, sec)
	require.NoError(t, err)
	assert.Equal(t, hunt.OutcomeMerged, outcome3)
	v4, seen4 := version()
	assert.NotEqual(t, v3, v4, "stale last_seen_at must be refreshed")
	assert.WithinDuration(t, time.Now(), seen4, time.Minute)

	// A status/closed_at change is written even inside the window.
	closedAt := time.Now()
	closed := sec
	closed.Status, closed.ClosedAt = "closed", &closedAt
	// status only moves while the stored row is 'open'; closed_at fills in.
	_, _, err = s.UpsertSecurity(ctx, closed)
	require.NoError(t, err)
	var status string
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM hunt_security WHERE id = $1`, id).Scan(&status))
	assert.Equal(t, "closed", status, "status change must still be written inside the refresh window")
}
