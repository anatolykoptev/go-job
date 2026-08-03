package score

import (
	"testing"
	"time"

	"github.com/anatolykoptev/go-kit/env"
	"github.com/stretchr/testify/assert"
)

// TestScoringSettings_FailOpenDefault verifies the #167 fix: fail-open defaults
// to false when both DB setting and env are unset.
func TestScoringSettings_FailOpenDefault(t *testing.T) {
	s := &ScoringSettings{}
	assert.False(t, s.failOpen(), "default fail-open is false per #167")
}

// TestScoringSettings_FailOpen_DBWins verifies a non-nil DB FailOpen pointer
// overrides env.
func TestScoringSettings_FailOpen_DBWins(t *testing.T) {
	t.Setenv("HUNT_SCORE_FAIL_OPEN", "false")
	dbVal := true
	s := &ScoringSettings{FailOpen: &dbVal}
	assert.True(t, s.failOpen(), "DB true overrides env false")
}

// TestScoringSettings_FailOpen_EnvFallback verifies env is used when DB
// pointer is nil.
func TestScoringSettings_FailOpen_EnvFallback(t *testing.T) {
	t.Setenv("HUNT_SCORE_FAIL_OPEN", "true")
	s := &ScoringSettings{}
	assert.True(t, s.failOpen(), "env true used when DB nil")
}

// TestScoringSettings_NotifyMaxAge_DBWins verifies DB NotifyMaxAge overrides env.
func TestScoringSettings_NotifyMaxAge_DBWins(t *testing.T) {
	t.Setenv("HUNT_NOTIFY_MAX_AGE", "48h")
	s := &ScoringSettings{NotifyMaxAge: 12 * time.Hour}
	assert.Equal(t, 12*time.Hour, s.maxAge(), "DB 12h overrides env 48h")
}

// TestScoringSettings_NotifyMaxAge_EnvFallback verifies env fallback when DB is zero.
func TestScoringSettings_NotifyMaxAge_EnvFallback(t *testing.T) {
	t.Setenv("HUNT_NOTIFY_MAX_AGE", "24h")
	s := &ScoringSettings{}
	assert.Equal(t, 24*time.Hour, s.maxAge(), "env 24h used when DB zero")
}

// TestScoringSettings_NotifyMaxAge_NilSettings verifies nil settings uses env.
func TestScoringSettings_NilSettings(t *testing.T) {
	t.Setenv("HUNT_NOTIFY_MAX_AGE", "48h")
	t.Setenv("HUNT_SCORE_MIN_JACCARD", "12")
	t.Setenv("HUNT_SCORE_MAX_LLM_PER_CYCLE", "25")
	var s *ScoringSettings // nil
	assert.Equal(t, 48*time.Hour, s.maxAge())
	assert.InDelta(t, 12.0, s.minJaccard(), 0.01)
	assert.Equal(t, 25, s.maxLLMPerCycle())
	assert.False(t, s.failOpen(), "nil settings → env default false")
}

// TestScoringSettings_MinJaccard_DBWins verifies DB MinJaccard overrides env.
func TestScoringSettings_MinJaccard_DBWins(t *testing.T) {
	t.Setenv("HUNT_SCORE_MIN_JACCARD", "8")
	s := &ScoringSettings{MinJaccard: 20}
	assert.InDelta(t, 20.0, s.minJaccard(), 0.01, "DB 20 overrides env 8")
}

// TestScoringSettings_MaxLLMPerCycle_DBWins verifies DB MaxLLMPerCycle overrides env.
func TestScoringSettings_MaxLLMPerCycle_DBWins(t *testing.T) {
	t.Setenv("HUNT_SCORE_MAX_LLM_PER_CYCLE", "50")
	s := &ScoringSettings{MaxLLMPerCycle: 10}
	assert.Equal(t, 10, s.maxLLMPerCycle(), "DB 10 overrides env 50")
}

// TestScoringSettings_MinQuality_DBWins verifies DB MinQuality overrides env.
func TestScoringSettings_MinQuality_DBWins(t *testing.T) {
	t.Setenv("HUNT_SCORE_MIN_QUALITY", "30")
	s := &ScoringSettings{MinQuality: 50}
	assert.Equal(t, 50, s.minQuality(), "DB 50 overrides env 30")
}

// TestScoringSettings_EnvUnset_UsesCodeDefaults verifies the code-level defaults
// when both DB and env are unset.
func TestScoringSettings_EnvUnset_UsesCodeDefaults(t *testing.T) {
	// Ensure env vars are unset for this test.
	t.Setenv("HUNT_NOTIFY_MAX_AGE", "")
	t.Setenv("HUNT_SCORE_MIN_JACCARD", "")
	t.Setenv("HUNT_SCORE_MIN_QUALITY", "")
	t.Setenv("HUNT_SCORE_MAX_LLM_PER_CYCLE", "")
	t.Setenv("HUNT_SCORE_FAIL_OPEN", "")
	s := &ScoringSettings{}
	assert.Equal(t, 48*time.Hour, s.maxAge(), "default maxAge 48h")
	assert.InDelta(t, defaultMinJaccard, s.minJaccard(), 0.01, "default minJaccard")
	assert.Equal(t, defaultMinQuality, s.minQuality(), "default minQuality")
	assert.Equal(t, 50, s.maxLLMPerCycle(), "default maxLLM 50")
	assert.False(t, s.failOpen(), "default failOpen false")
}

// suppress unused import warning if env is only used transitively.
var _ = env.Int
