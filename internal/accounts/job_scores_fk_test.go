package accounts_test

import (
	"context"
	"testing"

	"github.com/anatolykoptev/go_job/internal/accounts"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEnsureJobScoresJobFK: orphan scores are deleted and counted, the FK is
// installed, and deleting a job then cascades to its scores. Mutations:
// drop ON DELETE CASCADE -> the job delete is refused (RED); drop the orphan
// DELETE -> ADD CONSTRAINT fails (RED).
func TestEnsureJobScoresJobFK(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	_, _, err := accounts.Bootstrap(ctx, pool, accounts.OperatorSeed{})
	require.NoError(t, err)
	require.NoError(t, hunt.NewStore(pool).Migrate(ctx))
	aid, _, err := accounts.CreateAccount(ctx, pool,
		"fk-test-"+uuid.NewString()[:12]+"@example.com", "fk test", "test-hash", "user")
	require.NoError(t, err)

	// Back to the pre-FK state this change finds in production.
	_, err = pool.Exec(ctx, `ALTER TABLE account_job_scores DROP CONSTRAINT IF EXISTS account_job_scores_job_id_fkey`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `TRUNCATE hunt_jobs CASCADE`)
	require.NoError(t, err)
	var jobID int64
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO hunt_jobs (dedup_hash, title, url, source) VALUES ('fk-test', 't', 'https://fk.example', 's')
		RETURNING id`).Scan(&jobID))
	const orphanJobID = 987654321
	for _, id := range []int64{jobID, orphanJobID, orphanJobID + 1} {
		_, err = pool.Exec(ctx, `INSERT INTO account_job_scores (account_id, job_id, fit_score) VALUES ($1, $2, 1)`, aid, id)
		require.NoError(t, err)
	}

	orphans, err := accounts.EnsureJobScoresJobFK(ctx, pool)
	require.NoError(t, err)
	assert.Equal(t, int64(2), orphans, "orphan scores are deleted and counted")

	again, err := accounts.EnsureJobScoresJobFK(ctx, pool)
	require.NoError(t, err)
	assert.Zero(t, again, "second run is a no-op")

	_, err = pool.Exec(ctx, `INSERT INTO account_job_scores (account_id, job_id, fit_score) VALUES ($1, $2, 1)`, aid, orphanJobID)
	assert.Error(t, err, "FK must now refuse an orphan score")

	_, err = pool.Exec(ctx, `DELETE FROM hunt_jobs WHERE id = $1`, jobID)
	require.NoError(t, err, "deleting a job with scores must cascade, not be refused")
	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM account_job_scores WHERE job_id = $1`, jobID).Scan(&n))
	assert.Zero(t, n, "scores of a deleted job are removed")
}
