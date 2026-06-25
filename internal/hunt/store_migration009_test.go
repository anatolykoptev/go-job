package hunt_test

import (
	"context"
	"testing"

	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStore_Migration009_Idempotent verifies migration 009 absorbs the
// recommendation columns + the fit_score index that go-nerv's 007/008
// migrations previously owned (ADR-go-job-001: go-job is the sole DDL owner of
// hunt_*). It asserts the columns + both indexes exist and Migrate is idempotent.
func TestStore_Migration009_Idempotent(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)

	require.NoError(t, s.Migrate(ctx), "first migrate")
	require.NoError(t, s.Migrate(ctx), "second migrate must be idempotent")

	cols := []string{"recommendation_tier", "recommendation_rank", "recommendation_note"}
	for _, col := range cols {
		var exists bool
		err := pool.QueryRow(ctx,
			`SELECT EXISTS(
				SELECT 1 FROM information_schema.columns
				WHERE table_name='hunt_jobs' AND column_name=$1
			)`, col).Scan(&exists)
		require.NoError(t, err)
		assert.True(t, exists, "column %s must exist after migration 009", col)
	}

	idx := []string{"hunt_jobs_fit_score_idx", "hunt_jobs_recommendation_rank_idx"}
	for _, name := range idx {
		var exists bool
		err := pool.QueryRow(ctx,
			`SELECT EXISTS(
				SELECT 1 FROM pg_indexes
				WHERE tablename='hunt_jobs' AND indexname=$1
			)`, name).Scan(&exists)
		require.NoError(t, err)
		assert.True(t, exists, "index %s must exist after migration 009", name)
	}
}
