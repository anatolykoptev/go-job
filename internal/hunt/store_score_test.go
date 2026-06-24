package hunt_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStore_Migration008_Idempotent verifies that migration 008 adds all 6 score
// columns and is idempotent (safe to run twice).
func TestStore_Migration008_Idempotent(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)

	// Run twice — must not error
	require.NoError(t, s.Migrate(ctx), "first migrate")
	require.NoError(t, s.Migrate(ctx), "second migrate must be idempotent")

	// Verify all 6 score columns exist
	cols := []string{"fit_score", "fit_band", "success_band", "over_under", "score_rationale", "scored_at"}
	for _, col := range cols {
		var exists bool
		err := pool.QueryRow(ctx,
			`SELECT EXISTS(
				SELECT 1 FROM information_schema.columns
				WHERE table_name='hunt_jobs' AND column_name=$1
			)`, col).Scan(&exists)
		require.NoError(t, err)
		assert.True(t, exists, "column %s must exist after migration", col)
	}
}

// TestStore_SetJobScore_RoundTrip inserts a job, scores it, reads back the score
// columns directly and verifies: all fields round-trip correctly, status is
// untouched, and JSONB shape matches the scoreRationale contract.
func TestStore_SetJobScore_RoundTrip(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))
	truncateJobs(t, pool)

	// Insert a job via UpsertJob
	j := hunt.Job{
		DedupHash: hunt.DedupHash("https://example.com/jobs/score-test"),
		Title:     "Senior Go Engineer",
		URL:       "https://example.com/jobs/score-test",
		Source:    "test",
		Status:    hunt.StatusOpen,
	}
	id, outcome, err := s.UpsertJob(ctx, j)
	require.NoError(t, err)
	assert.Equal(t, hunt.OutcomeCreated, outcome)

	scoredAt := time.Now().UTC().Truncate(time.Second)
	sr := hunt.ScoreResult{
		FitScore:         82,
		FitBand:          "high",
		SuccessBand:      "STRONG",
		OverUnder:        "well_matched",
		FitReasons:       []string{"Go stack", "remote-first"},
		FitGaps:          []string{"no k8s experience listed"},
		SuccessReasoning: "Strong stack match; seniority aligns well.",
		ScoredAt:         scoredAt,
	}

	err = s.SetJobScore(ctx, id, sr)
	require.NoError(t, err, "SetJobScore must not error")

	// Read back score columns directly (GetJob does not yet expose them)
	var fitScore *int
	var fitBand, successBand, overUnder *string
	var rationaleJSON []byte
	var dbScoredAt *time.Time
	var dbStatus string
	err = pool.QueryRow(ctx, `
		SELECT fit_score, fit_band, success_band, over_under,
		       score_rationale, scored_at, status
		FROM hunt_jobs WHERE id = $1`, id).Scan(
		&fitScore, &fitBand, &successBand, &overUnder,
		&rationaleJSON, &dbScoredAt, &dbStatus,
	)
	require.NoError(t, err)

	require.NotNil(t, fitScore, "fit_score must be set")
	assert.Equal(t, 82, *fitScore)
	require.NotNil(t, fitBand)
	assert.Equal(t, "high", *fitBand)
	require.NotNil(t, successBand)
	assert.Equal(t, "STRONG", *successBand)
	require.NotNil(t, overUnder)
	assert.Equal(t, "well_matched", *overUnder)
	require.NotNil(t, dbScoredAt)
	assert.Equal(t, scoredAt, dbScoredAt.UTC().Truncate(time.Second))

	// status must be unchanged
	assert.Equal(t, hunt.StatusOpen, dbStatus, "SetJobScore must NOT change status")

	// Verify JSONB shape
	var rat struct {
		FitReasons       []string `json:"fit_reasons"`
		FitGaps          []string `json:"fit_gaps"`
		SuccessReasoning string   `json:"success_reasoning"`
	}
	require.NoError(t, json.Unmarshal(rationaleJSON, &rat))
	assert.Equal(t, []string{"Go stack", "remote-first"}, rat.FitReasons)
	assert.Equal(t, []string{"no k8s experience listed"}, rat.FitGaps)
	assert.Equal(t, "Strong stack match; seniority aligns well.", rat.SuccessReasoning)
}

// TestStore_SetJobScore_NotFound verifies ErrNotFound is returned for unknown id.
func TestStore_SetJobScore_NotFound(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx))

	err := s.SetJobScore(ctx, 999999999, hunt.ScoreResult{ScoredAt: time.Now()})
	assert.ErrorIs(t, err, hunt.ErrNotFound, "SetJobScore on unknown id must return ErrNotFound")
}
