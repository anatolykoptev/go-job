package huntworker

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/accounts"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMergeAccountSettings_EnvDefaults verifies env fallbacks on a zero-value
// row: every scalar field fills from env, bools stay as the row set them.
func TestMergeAccountSettings_EnvDefaults(t *testing.T) {
	t.Setenv("HUNT_INGEST_QUERIES", "software engineer,backend engineer")
	t.Setenv("HUNT_NOTIFY_CHAT_ID", "777")
	t.Setenv("HUNT_NOTIFY_MIN_FIT", "0")
	t.Setenv("HUNT_NOTIFY_MAX_AGE", "48h")
	t.Setenv("HUNT_SCORE_MIN_JACCARD", "8")

	s := mergeAccountSettings(accounts.AccountHuntSettings{
		HasRow:  true,
		Enabled: true,
	})
	assert.True(t, s.Enabled)
	assert.Equal(t, "software engineer,backend engineer", s.Queries)
	assert.Equal(t, int64(777), s.NotifyChatID)
	assert.Equal(t, 0, s.NotifyMinFit)
	assert.Equal(t, 48*time.Hour, s.NotifyMaxAge)
	assert.Equal(t, 8, s.ScoreMinJaccard)
	assert.Nil(t, s.ScoreMaxLLMPerCycle, "unset sub-cap stays nil (fleet cap only)")
}

// TestMergeAccountSettings_RowWins verifies non-zero row fields beat env —
// the same per-field merge contract the legacy single-row LoadSettings had.
func TestMergeAccountSettings_RowWins(t *testing.T) {
	t.Setenv("HUNT_INGEST_QUERIES", "software engineer")
	t.Setenv("HUNT_NOTIFY_MIN_FIT", "0")

	maxLLM := 25
	s := mergeAccountSettings(accounts.AccountHuntSettings{
		HasRow:              true,
		Enabled:             true,
		Queries:             "rust developer,distributed systems engineer",
		NotifyMinFit:        60,
		NotifyMaxAge:        24 * time.Hour,
		ScoreEnabled:        true,
		ScoreMinJaccard:     12,
		ScoreMaxLLMPerCycle: &maxLLM,
		ScoreFailOpen:       false,
	})
	assert.Equal(t, "rust developer,distributed systems engineer", s.Queries, "row queries win")
	assert.Equal(t, 60, s.NotifyMinFit, "row notify_min_fit wins")
	assert.Equal(t, 24*time.Hour, s.NotifyMaxAge, "row notify_max_age wins")
	assert.Equal(t, 12, s.ScoreMinJaccard, "row score_min_jaccard wins")
	require.NotNil(t, s.ScoreMaxLLMPerCycle)
	assert.Equal(t, 25, *s.ScoreMaxLLMPerCycle)
	// Bools are row-authoritative — env can never re-arm a disabled account.
	assert.True(t, s.Enabled)
	assert.True(t, s.ScoreEnabled)
	assert.False(t, s.ScoreFailOpen, "row score_fail_open wins over env")
}

// TestMergeAccountSettings_BoolsRowAuthoritative pins the fail-closed rule:
// env must never re-enable an account whose row says disabled — arming a
// hunt is a row-write, not an env var.
func TestMergeAccountSettings_BoolsRowAuthoritative(t *testing.T) {
	t.Setenv("HUNT_SCORE_FAIL_OPEN", "true")
	t.Setenv("HUNT_SCORE_ENABLED", "true")

	s := mergeAccountSettings(accounts.AccountHuntSettings{
		HasRow:        true,
		Enabled:       false,
		ScoreEnabled:  false,
		ScoreFailOpen: false,
	})
	assert.False(t, s.Enabled)
	assert.False(t, s.ScoreEnabled)
	assert.False(t, s.ScoreFailOpen, "env fail_open must not override row=false")
}

// TestMergeAccountSettings_NotifyMinFitClamped verifies out-of-range clamps.
func TestMergeAccountSettings_NotifyMinFitClamped(t *testing.T) {
	t.Setenv("HUNT_NOTIFY_MIN_FIT", "150")
	s := mergeAccountSettings(accounts.AccountHuntSettings{HasRow: true})
	assert.Equal(t, 100, s.NotifyMinFit, "env clamped to 100")

	s = mergeAccountSettings(accounts.AccountHuntSettings{HasRow: true, NotifyMinFit: 150})
	assert.Equal(t, 100, s.NotifyMinFit, "row clamped to 100")
}

// TestLoadAccountPlans_PerAccount verifies the full census path: enabled
// accounts produce plans with env-merged settings, effective LLM sub-caps,
// bound facades and account-routed notifiers.
func TestLoadAccountPlans_PerAccount(t *testing.T) {
	t.Setenv("HUNT_SCORE_MAX_LLM_PER_CYCLE", "50")

	aidA := uuid.New()
	aidB := uuid.New()
	subCap := 10

	var boundFor []uuid.UUID
	var notifyRoutes []int64

	w := &Worker{}
	w.listAccounts = func(context.Context) ([]accounts.AccountHuntSettings, error) {
		return []accounts.AccountHuntSettings{
			{AccountID: aidA, HasRow: true, Enabled: true,
				ScoreEnabled: true, ScoreMaxLLMPerCycle: &subCap,
				NotifyChatID: 111, NotifyMinFit: 70},
			{AccountID: aidB, HasRow: true, Enabled: true,
				ScoreEnabled: true, NotifyChatID: 222},
		}, nil
	}
	w.forAccount = func(aid uuid.UUID) accountScoreStore {
		boundFor = append(boundFor, aid)
		return nil
	}
	w.accountNotify = func(chatID int64, _ time.Duration) hunt.Notifier {
		notifyRoutes = append(notifyRoutes, chatID)
		return nil
	}

	var fleet atomic.Int64
	plans := w.loadAccountPlans(context.Background(), &fleet)
	require.Len(t, plans, 2)

	assert.Equal(t, aidA, plans[0].aid)
	assert.Equal(t, aidB, plans[1].aid)
	assert.Equal(t, []uuid.UUID{aidA, aidB}, boundFor, "facade bound per account")
	assert.Equal(t, []int64{111, 222}, notifyRoutes, "notifier routed to each account's chat id")

	// A's sub-cap tightens the fleet cap; B (NULL) shares the fleet cap.
	assert.Equal(t, 10, plans[0].deps.Settings.MaxLLMPerCycle)
	assert.Equal(t, 50, plans[1].deps.Settings.MaxLLMPerCycle)
	// Both budgets share the SAME fleet counter.
	assert.Same(t, &fleet, plans[0].budget.fleet)
	assert.Same(t, &fleet, plans[1].budget.fleet)
	assert.Equal(t, 50, plans[0].budget.fleetCap)
}
