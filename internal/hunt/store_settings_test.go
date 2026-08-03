package hunt_test

import (
	"context"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStore_HuntSettings_RoundTrip verifies GetHuntSettings returns what
// SaveHuntSettings wrote, and that a fresh DB (no row) returns zero-value
// defaults.
func TestStore_HuntSettings_RoundTrip(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx), "migrate")

	// Clean slate — ensure no row from a prior test.
	_, _ = pool.Exec(ctx, `DELETE FROM hunt_settings`)

	// No row → zero-value HuntSettings (all defaults), no error.
	got, err := s.GetHuntSettings(ctx)
	require.NoError(t, err)
	assert.True(t, got.UpdatedAt.IsZero(), "zero-value row has zero UpdatedAt")

	// Save a full settings row.
	want := hunt.HuntSettings{
		Enabled:             true,
		Interval:            3 * time.Hour,
		Queries:             "golang developer,backend engineer",
		NotifyChatID:        123456789,
		NotifyMinFit:        60,
		NotifyMaxAge:        24 * time.Hour,
		ScoreEnabled:        true,
		ScoreMinJaccard:     10,
		ScoreMaxLLMPerCycle: 30,
		ScoreSweepLimit:     40,
		ScoreFailOpen:       false,
	}
	require.NoError(t, s.SaveHuntSettings(ctx, want), "save")

	// Read back — all fields match.
	got, err = s.GetHuntSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, want.Enabled, got.Enabled)
	assert.Equal(t, want.Interval, got.Interval)
	assert.Equal(t, want.Queries, got.Queries)
	assert.Equal(t, want.NotifyChatID, got.NotifyChatID)
	assert.Equal(t, want.NotifyMinFit, got.NotifyMinFit)
	assert.Equal(t, want.NotifyMaxAge, got.NotifyMaxAge)
	assert.Equal(t, want.ScoreEnabled, got.ScoreEnabled)
	assert.Equal(t, want.ScoreMinJaccard, got.ScoreMinJaccard)
	assert.Equal(t, want.ScoreMaxLLMPerCycle, got.ScoreMaxLLMPerCycle)
	assert.Equal(t, want.ScoreSweepLimit, got.ScoreSweepLimit)
	assert.Equal(t, want.ScoreFailOpen, got.ScoreFailOpen)
	assert.False(t, got.UpdatedAt.IsZero(), "UpdatedAt set on save")

	// Upsert — save again with different values, row count stays 1.
	want.NotifyMinFit = 80
	want.Queries = "rust developer"
	require.NoError(t, s.SaveHuntSettings(ctx, want), "upsert")
	var rowCount int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM hunt_settings`).Scan(&rowCount))
	assert.Equal(t, 1, rowCount, "single row after upsert")
	got, err = s.GetHuntSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, 80, got.NotifyMinFit)
	assert.Equal(t, "rust developer", got.Queries)
}
