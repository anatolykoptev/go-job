package huntworker

import (
	"context"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/stretchr/testify/assert"
)

// fakeSettingsStore implements huntSettingsStore for LoadSettings tests.
type fakeSettingsStore struct {
	settings hunt.HuntSettings
	err      error
}

func (f *fakeSettingsStore) GetHuntSettings(ctx context.Context) (hunt.HuntSettings, error) {
	return f.settings, f.err
}

// TestLoadSettings_NilStore_UsesEnvDefaults verifies that with no DB store,
// all settings come from env vars with the documented defaults.
func TestLoadSettings_NilStore_UsesEnvDefaults(t *testing.T) {
	t.Setenv("HUNT_INGEST_ENABLED", "true")
	t.Setenv("HUNT_INGEST_INTERVAL", "6h")
	t.Setenv("HUNT_INGEST_QUERIES", "software engineer,backend engineer,golang developer")
	t.Setenv("HUNT_NOTIFY_MIN_FIT", "0")
	t.Setenv("HUNT_NOTIFY_MAX_AGE", "48h")
	t.Setenv("HUNT_SCORE_ENABLED", "true")
	t.Setenv("HUNT_SCORE_MIN_JACCARD", "8")
	t.Setenv("HUNT_SCORE_MAX_LLM_PER_CYCLE", "50")
	t.Setenv("HUNT_SCORE_SWEEP_LIMIT", "50")
	t.Setenv("HUNT_SCORE_FAIL_OPEN", "true")

	s := LoadSettings(context.Background(), nil)
	assert.True(t, s.Enabled)
	assert.Equal(t, 6*time.Hour, s.Interval)
	assert.Equal(t, "software engineer,backend engineer,golang developer", s.Queries)
	assert.True(t, s.ScoreEnabled)
	assert.True(t, s.ScoreFailOpen)
	assert.Equal(t, 8, s.ScoreMinJaccard)
}

// TestLoadSettings_DBOverridesEnv verifies DB values win over env when non-zero.
func TestLoadSettings_DBOverridesEnv(t *testing.T) {
	t.Setenv("HUNT_INGEST_INTERVAL", "6h")
	t.Setenv("HUNT_INGEST_QUERIES", "software engineer")
	t.Setenv("HUNT_NOTIFY_MIN_FIT", "0")

	store := &fakeSettingsStore{
		settings: hunt.HuntSettings{
			Enabled:             true,
			Interval:            2 * time.Hour,
			Queries:             "rust developer,distributed systems engineer",
			NotifyMinFit:        60,
			NotifyMaxAge:        24 * time.Hour,
			ScoreEnabled:        true,
			ScoreMinJaccard:     12,
			ScoreMaxLLMPerCycle: 25,
			ScoreSweepLimit:     35,
			ScoreFailOpen:       false,
		},
	}
	// UpdatedAt non-zero → bool fields come from DB.
	store.settings.UpdatedAt = time.Now()

	s := LoadSettings(context.Background(), store)
	assert.Equal(t, 2*time.Hour, s.Interval, "DB interval wins")
	assert.Equal(t, "rust developer,distributed systems engineer", s.Queries, "DB queries win")
	assert.Equal(t, 60, s.NotifyMinFit, "DB notify_min_fit wins")
	assert.Equal(t, 24*time.Hour, s.NotifyMaxAge, "DB notify_max_age wins")
	assert.Equal(t, 12, s.ScoreMinJaccard, "DB score_min_jaccard wins")
	assert.Equal(t, 25, s.ScoreMaxLLMPerCycle, "DB score_max_llm wins")
	assert.Equal(t, 35, s.ScoreSweepLimit, "DB score_sweep_limit wins")
	assert.True(t, s.Enabled, "DB enabled wins")
	assert.True(t, s.ScoreEnabled, "DB score_enabled wins")
	assert.False(t, s.ScoreFailOpen, "DB score_fail_open wins")
}

// TestLoadSettings_DBZeroKeepsEnvDefault verifies that zero-value DB fields
// fall back to env defaults (the merge contract).
func TestLoadSettings_DBZeroKeepsEnvDefault(t *testing.T) {
	t.Setenv("HUNT_INGEST_INTERVAL", "6h")
	t.Setenv("HUNT_INGEST_QUERIES", "software engineer,backend engineer")
	t.Setenv("HUNT_NOTIFY_MIN_FIT", "0")

	// DB row absent → zero-value HuntSettings (UpdatedAt zero).
	store := &fakeSettingsStore{}

	s := LoadSettings(context.Background(), store)
	assert.Equal(t, 6*time.Hour, s.Interval, "env interval kept when DB is zero")
	assert.Equal(t, "software engineer,backend engineer", s.Queries, "env queries kept when DB is empty")
	assert.Equal(t, 0, s.NotifyMinFit, "env notify_min_fit kept when DB is zero")
}

// TestLoadSettings_NotifyMinFitClamped verifies the clamp on out-of-range values.
func TestLoadSettings_NotifyMinFitClamped(t *testing.T) {
	t.Setenv("HUNT_NOTIFY_MIN_FIT", "150")
	s := LoadSettings(context.Background(), nil)
	assert.Equal(t, 100, s.NotifyMinFit, "clamped to 100")

	t.Setenv("HUNT_NOTIFY_MIN_FIT", "-5")
	s = LoadSettings(context.Background(), nil)
	assert.Equal(t, 0, s.NotifyMinFit, "clamped to 0")
}

// TestLoadSettings_DBError_UsesEnvDefaults verifies that a DB error falls back
// to env defaults (fail-soft, not fail-dead).
func TestLoadSettings_DBError_UsesEnvDefaults(t *testing.T) {
	t.Setenv("HUNT_INGEST_INTERVAL", "6h")
	store := &fakeSettingsStore{err: assertError("db down")}
	s := LoadSettings(context.Background(), store)
	assert.Equal(t, 6*time.Hour, s.Interval, "env interval used on DB error")
}

type assertError string

func (e assertError) Error() string { return string(e) }
