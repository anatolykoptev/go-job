package hunt_test

import (
	"context"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/accounts"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStore_HuntSettings_RoundTrip verifies the per-account settings
// contract (P3, ADR-7): GetHuntSettings on the bound facade returns what
// SaveHuntSettings wrote, a missing row reports HasRow=false with
// disabled/fail-closed defaults, and a second account's row is invisible —
// writes are keyed by the facade-bound account id, never reachable
// cross-account.
func TestStore_HuntSettings_RoundTrip(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx), "migrate")

	acctA := s.ForAccount(newScoreAccount(t, pool))
	acctB := s.ForAccount(newScoreAccount(t, pool))

	// Clean slate for A (defensive — fresh accounts start empty anyway).
	_, _ = pool.Exec(ctx, `DELETE FROM account_hunt_settings WHERE account_id = $1`, acctA.AccountID())

	// No row → HasRow=false, disabled/fail-closed defaults (a missing row must
	// never render enabled — provisioning does not arm a hunt).
	got, hasRow, err := acctA.GetHuntSettings(ctx)
	require.NoError(t, err)
	assert.False(t, hasRow, "fresh account has no settings row")
	assert.False(t, got.Enabled)
	assert.False(t, got.ScoreEnabled)
	assert.False(t, got.ScoreFailOpen, "missing settings row is fail-closed")
	assert.True(t, got.UpdatedAt.IsZero(), "zero-value row has zero UpdatedAt")

	// Save a full settings row for A.
	maxLLM := 30
	want := accounts.AccountHuntSettings{
		Enabled:             true,
		Queries:             "golang developer,backend engineer",
		NotifyChatID:        123456789,
		NotifyMinFit:        60,
		NotifyMaxAge:        24 * time.Hour,
		ScoreEnabled:        true,
		ScoreMinJaccard:     10,
		ScoreMaxLLMPerCycle: &maxLLM,
		ScoreFailOpen:       true,
	}
	require.NoError(t, acctA.SaveHuntSettings(ctx, want), "save")

	// Read back — all fields match.
	got, hasRow, err = acctA.GetHuntSettings(ctx)
	require.NoError(t, err)
	require.True(t, hasRow)
	assert.Equal(t, want.Enabled, got.Enabled)
	assert.Equal(t, want.Queries, got.Queries)
	assert.Equal(t, want.NotifyChatID, got.NotifyChatID)
	assert.Equal(t, want.NotifyMinFit, got.NotifyMinFit)
	assert.Equal(t, want.NotifyMaxAge, got.NotifyMaxAge)
	assert.Equal(t, want.ScoreEnabled, got.ScoreEnabled)
	assert.Equal(t, want.ScoreMinJaccard, got.ScoreMinJaccard)
	require.NotNil(t, got.ScoreMaxLLMPerCycle)
	assert.Equal(t, *want.ScoreMaxLLMPerCycle, *got.ScoreMaxLLMPerCycle)
	assert.Equal(t, want.ScoreFailOpen, got.ScoreFailOpen)
	assert.False(t, got.UpdatedAt.IsZero(), "UpdatedAt set on save")

	// Isolation: B has no row — A's settings are invisible to B (and a NULL
	// sub-cap reads back nil).
	_, hasRowB, err := acctB.GetHuntSettings(ctx)
	require.NoError(t, err)
	assert.False(t, hasRowB, "account B must not see A's settings row")

	// Upsert for A — same row key, still one row per account.
	want.NotifyMinFit = 80
	want.Queries = "rust developer"
	require.NoError(t, acctA.SaveHuntSettings(ctx, want), "upsert")
	var rowCount int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM account_hunt_settings WHERE account_id = $1`,
		acctA.AccountID()).Scan(&rowCount))
	assert.Equal(t, 1, rowCount, "one row per account after upsert")
	got, _, err = acctA.GetHuntSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, 80, got.NotifyMinFit)
	assert.Equal(t, "rust developer", got.Queries)

	// B writes its own row — A's row must be untouched.
	require.NoError(t, acctB.SaveHuntSettings(ctx, accounts.AccountHuntSettings{
		Enabled: true, Queries: "b queries", NotifyMinFit: 5,
	}))
	gotA, _, err := acctA.GetHuntSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, "rust developer", gotA.Queries, "B's write must not touch A's row")
	assert.Equal(t, 80, gotA.NotifyMinFit)
	gotB, hasRowB, err := acctB.GetHuntSettings(ctx)
	require.NoError(t, err)
	require.True(t, hasRowB)
	assert.Equal(t, "b queries", gotB.Queries)
}
