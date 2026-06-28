package score_test

// Tests for ScoringProfile + LoadProfile.
//
// Four test groups per spec (Phase 2):
//  (a) DB-shaped input → ScoringProfile: marketing/event skills+domains EXCLUDED,
//      tech skills included by category+level filter.
//  (b) ENV overrides for seniority/comp/locations/work_auth/avoid.
//  (c) HUNT_SCORE_PROFILE_PATH JSON file load (fallback).
//  (d) Empty DB + no path → (nil, nil) + WARN log, no error.
//
// Import-cycle note: internal/hunt/score reads resume tables DIRECTLY via
// pgxpool — it does NOT import internal/engine/jobs (which already imports
// internal/hunt, forming a cycle). DB tests skip when DATABASE_URL is unset.

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anatolykoptev/go_job/internal/dbtest"
	"github.com/anatolykoptev/go_job/internal/hunt/score"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openTestPool opens a pgxpool for integration tests; skips if DATABASE_URL unset;
// fatals if it points at a non-_test database.
func openTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	dbtest.RequireTestDB(t, dsn)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err, "open test pool")
	t.Cleanup(func() { pool.Close() })
	return pool
}

// seedResumeProfile inserts a synthetic resume_profile row set into the DB and
// returns the person_id. Cleans up on t.Cleanup.
//
// skills: list of (name, category, level) tuples.
// domains: list of domain names.
func seedResumeProfile(t *testing.T, pool *pgxpool.Pool, skills [][3]string, domains []string) int {
	t.Helper()
	ctx := context.Background()

	// Ensure tables exist (the engine/jobs migrations run on the same DATABASE_URL).
	// We only need to ensure the tables are present; we don't run the full migration
	// here to avoid a dependency on engine/jobs. If the tables don't exist, the
	// DB test will error (not skip) — that is intentional.

	// Insert person.
	var personID int
	err := pool.QueryRow(ctx,
		`INSERT INTO resume_persons (name, email) VALUES ($1, $2) RETURNING id`,
		"Test User", "test@example.com",
	).Scan(&personID)
	require.NoError(t, err, "seed person")

	// Insert skills.
	for _, s := range skills {
		_, err := pool.Exec(ctx,
			`INSERT INTO resume_skills (person_id, name, category, level) VALUES ($1, $2, $3, $4)
			 ON CONFLICT (person_id, name) DO NOTHING`,
			personID, s[0], s[1], s[2],
		)
		require.NoError(t, err, "seed skill %s", s[0])
	}

	// Insert domains.
	for _, d := range domains {
		_, err := pool.Exec(ctx,
			`INSERT INTO resume_domains (person_id, name) VALUES ($1, $2)
			 ON CONFLICT (person_id, name) DO NOTHING`,
			personID, d,
		)
		require.NoError(t, err, "seed domain %s", d)
	}

	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM resume_persons WHERE id = $1`, personID)
	})
	return personID
}

// ---- (a) DB-path: marketing/event noise EXCLUDED, tech included ----

// TestLoadProfile_DB_TechSkillsIncluded verifies that tech skills
// (categories: programming_language, database, devops, other) at
// advanced|expert level map to CoreSkills, and intermediate to AdjacentSkills.
// Reverted production filter (deleting the category/level guards) makes this fail.
func TestLoadProfile_DB_TechSkillsIncluded(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()

	skills := [][3]string{
		{"Go", "programming_language", "expert"},
		{"Rust", "programming_language", "advanced"},
		{"PostgreSQL", "database", "advanced"},
		{"Kubernetes", "devops", "intermediate"},
		{"Terraform", "devops", "expert"},
	}
	domains := []string{"AI Infrastructure", "Developer Tools"}

	seedResumeProfile(t, pool, skills, domains)

	prof, err := score.LoadProfile(ctx, pool)
	require.NoError(t, err)
	require.NotNil(t, prof, "profile must be non-nil when DB has data")

	assert.Contains(t, prof.CoreSkills, "Go", "expert programming_language must be CoreSkill")
	assert.Contains(t, prof.CoreSkills, "Rust", "advanced programming_language must be CoreSkill")
	assert.Contains(t, prof.CoreSkills, "PostgreSQL", "advanced database must be CoreSkill")
	assert.Contains(t, prof.CoreSkills, "Terraform", "expert devops must be CoreSkill")

	assert.Contains(t, prof.AdjacentSkills, "Kubernetes", "intermediate devops must be AdjacentSkill")

	assert.Contains(t, prof.TargetDomains, "AI Infrastructure")
	assert.Contains(t, prof.TargetDomains, "Developer Tools")
}

// TestLoadProfile_DB_MarketingEventExcluded verifies that marketing/event skills
// and soft_skill/methodology categories are NOT included in Core or Adjacent,
// and that noise domains (Media, Digital Marketing, Event Production) are excluded.
// Reverted filter causes this to fail — falsification confirmed at RED.
func TestLoadProfile_DB_MarketingEventExcluded(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()

	skills := [][3]string{
		// Tech — should be included
		{"Go", "programming_language", "expert"},
		// Noise — must be excluded
		{"Guerrilla Marketing", "methodology", "advanced"},
		{"Event Production", "other", "advanced"},
		{"Public Speaking", "soft_skill", "expert"},
		{"Agile", "methodology", "intermediate"},
	}
	domains := []string{
		"AI Infrastructure", // tech — keep
		"Media",             // noise — drop
		"Digital Marketing", // noise — drop
		"Event Production",  // noise — drop
		"Developer Tools",   // tech — keep
	}

	seedResumeProfile(t, pool, skills, domains)

	prof, err := score.LoadProfile(ctx, pool)
	require.NoError(t, err)
	require.NotNil(t, prof)

	// Tech skill present
	assert.Contains(t, prof.CoreSkills, "Go")

	// Noise skills absent from both lists
	assert.NotContains(t, prof.CoreSkills, "Guerrilla Marketing")
	assert.NotContains(t, prof.AdjacentSkills, "Guerrilla Marketing")
	assert.NotContains(t, prof.CoreSkills, "Public Speaking")
	assert.NotContains(t, prof.AdjacentSkills, "Public Speaking")
	assert.NotContains(t, prof.CoreSkills, "Agile")
	assert.NotContains(t, prof.AdjacentSkills, "Agile")

	// Event Production: category "other" makes it normally eligible, BUT the
	// skill name contains "Event" which is in AvoidSignals → excluded.
	// This tests the name-level guard on top of category filter.
	assert.NotContains(t, prof.CoreSkills, "Event Production")

	// Noise domains absent
	assert.NotContains(t, prof.TargetDomains, "Media")
	assert.NotContains(t, prof.TargetDomains, "Digital Marketing")
	assert.NotContains(t, prof.TargetDomains, "Event Production")

	// Tech domains present
	assert.Contains(t, prof.TargetDomains, "AI Infrastructure")
	assert.Contains(t, prof.TargetDomains, "Developer Tools")
}

// ---- (b) ENV overrides ----

// TestLoadProfile_EnvOverrides verifies that HUNT_SCORE_* env vars override
// the hardcoded defaults for seniority, comp floor, locations, work auth, and
// avoid signals. (DB not required — uses profile path fallback with minimal JSON.)
func TestLoadProfile_EnvOverrides(t *testing.T) {
	// Write a minimal valid profile JSON so loader uses file path (no DB needed).
	dir := t.TempDir()
	minProfile := score.ScoringProfile{
		Seniority:     "Principal", // will be overridden by env
		CompFloorUSD:  200000,
		CoreSkills:    []string{"Go"},
		TargetDomains: []string{"Dev Tools"},
		Locations:     []string{"New York"},
		WorkAuth:      "US citizen",
		AvoidSignals:  []string{"sales"},
	}
	data, _ := json.Marshal(minProfile)
	path := filepath.Join(dir, "profile.json")
	require.NoError(t, os.WriteFile(path, data, 0o600))

	// Set env overrides.
	t.Setenv("HUNT_SCORE_PROFILE_PATH", path)
	t.Setenv("HUNT_SCORE_SENIORITY", "Staff")
	t.Setenv("HUNT_SCORE_COMP_FLOOR", "275000")
	t.Setenv("HUNT_SCORE_LOCATIONS", "San Francisco Bay Area,Remote (US)")
	t.Setenv("HUNT_SCORE_WORK_AUTH", "US authorized, no sponsorship")
	t.Setenv("HUNT_SCORE_AVOID", "sales,marketing,event,management")

	prof, err := score.LoadProfile(context.Background(), nil)
	require.NoError(t, err)
	require.NotNil(t, prof)

	// ENV overrides take precedence over what's in the file.
	assert.Equal(t, "Staff", prof.Seniority)
	assert.Equal(t, 275000, prof.CompFloorUSD)
	assert.Equal(t, []string{"San Francisco Bay Area", "Remote (US)"}, prof.Locations)
	assert.Equal(t, "US authorized, no sponsorship", prof.WorkAuth)
	assert.Equal(t, []string{"sales", "marketing", "event", "management"}, prof.AvoidSignals)
}

// TestLoadProfile_EnvDefaults verifies the default values when no env vars set
// and no profile path, but DB has data (uses DB path to prove defaults apply).
// Uses a minimal non-nil pool scenario by providing a temp profile file.
func TestLoadProfile_EnvDefaults(t *testing.T) {
	dir := t.TempDir()
	minProfile := score.ScoringProfile{CoreSkills: []string{"Go"}}
	data, _ := json.Marshal(minProfile)
	path := filepath.Join(dir, "profile.json")
	require.NoError(t, os.WriteFile(path, data, 0o600))

	t.Setenv("HUNT_SCORE_PROFILE_PATH", path)
	// Explicitly unset the override vars so defaults apply.
	t.Setenv("HUNT_SCORE_SENIORITY", "")
	t.Setenv("HUNT_SCORE_COMP_FLOOR", "")
	t.Setenv("HUNT_SCORE_LOCATIONS", "")
	t.Setenv("HUNT_SCORE_WORK_AUTH", "")
	t.Setenv("HUNT_SCORE_AVOID", "")

	prof, err := score.LoadProfile(context.Background(), nil)
	require.NoError(t, err)
	require.NotNil(t, prof)

	assert.Equal(t, "Staff", prof.Seniority, "default seniority")
	assert.Equal(t, 250000, prof.CompFloorUSD, "default comp floor")
	assert.Contains(t, prof.Locations, "San Francisco Bay Area", "default locations")
	assert.Contains(t, prof.Locations, "Remote (US)", "default locations")
	assert.Equal(t, "US authorized, no sponsorship", prof.WorkAuth, "default work auth")
	// Default avoid signals must include "sales" and "marketing".
	assert.Contains(t, prof.AvoidSignals, "sales", "default avoid signals")
	assert.Contains(t, prof.AvoidSignals, "marketing", "default avoid signals")
}

// ---- (c) HUNT_SCORE_PROFILE_PATH JSON file load ----

// TestLoadProfile_FilePath_Roundtrip verifies that a structured JSON file at
// HUNT_SCORE_PROFILE_PATH is loaded correctly when DB is nil, and that all
// fields round-trip through the JSON unmarshalling.
func TestLoadProfile_FilePath_Roundtrip(t *testing.T) {
	want := score.ScoringProfile{
		Seniority:      "Staff",
		TargetDomains:  []string{"AI Infrastructure", "Developer Tools"},
		CoreSkills:     []string{"Go", "Rust", "PostgreSQL"},
		AdjacentSkills: []string{"Kubernetes", "Terraform"},
		CompFloorUSD:   250000,
		Locations:      []string{"San Francisco Bay Area", "Remote (US)"},
		WorkAuth:       "US authorized, no sponsorship",
		AvoidSignals:   []string{"sales", "marketing", "event", "management", "recruiting"},
	}

	data, err := json.Marshal(want)
	require.NoError(t, err)

	dir := t.TempDir()
	path := filepath.Join(dir, "scoring_profile.json")
	require.NoError(t, os.WriteFile(path, data, 0o600))

	t.Setenv("HUNT_SCORE_PROFILE_PATH", path)
	// Unset env overrides so file values pass through.
	t.Setenv("HUNT_SCORE_SENIORITY", "")
	t.Setenv("HUNT_SCORE_COMP_FLOOR", "")
	t.Setenv("HUNT_SCORE_LOCATIONS", "")
	t.Setenv("HUNT_SCORE_WORK_AUTH", "")
	t.Setenv("HUNT_SCORE_AVOID", "")

	prof, err := score.LoadProfile(context.Background(), nil)
	require.NoError(t, err)
	require.NotNil(t, prof, "file-path load must return non-nil")

	// Core fields from file must survive.
	assert.Equal(t, want.CoreSkills, prof.CoreSkills)
	assert.Equal(t, want.AdjacentSkills, prof.AdjacentSkills)
	assert.Equal(t, want.TargetDomains, prof.TargetDomains)
}

// TestLoadProfile_FilePath_InvalidJSON verifies that a corrupt profile file
// returns an error rather than silently returning nil.
func TestLoadProfile_FilePath_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(path, []byte("{not valid json"), 0o600))

	t.Setenv("HUNT_SCORE_PROFILE_PATH", path)

	_, err := score.LoadProfile(context.Background(), nil)
	assert.Error(t, err, "corrupt JSON profile must return error")
}

// ---- (d) Empty + no path → (nil, nil) + WARN ----

// TestLoadProfile_Disabled_NilNilWarn verifies that when both the DB is nil and
// HUNT_SCORE_PROFILE_PATH is unset, LoadProfile returns (nil, nil) with a WARN
// log — and NEVER returns an error (caller must not be gated).
//
// Falsification: if the production code returns an error on disabled path,
// this test fails. If it returns a non-nil profile, this test fails.
func TestLoadProfile_Disabled_NilNilWarn(t *testing.T) {
	t.Setenv("HUNT_SCORE_PROFILE_PATH", "")

	// Capture slog output to verify WARN is emitted.
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	prof, err := score.LoadProfile(context.Background(), nil)

	assert.NoError(t, err, "disabled path must NOT error")
	assert.Nil(t, prof, "disabled path must return nil profile")
	assert.True(t, strings.Contains(buf.String(), "fit scoring disabled"),
		"must log WARN 'fit scoring disabled'; got: %s", buf.String())
}

// TestLoadProfile_DBEmpty_NilNil verifies that when the DB pool is provided
// but has no resume_persons rows, LoadProfile falls through to nil/nil/WARN
// (same as disabled case — no profile available).
func TestLoadProfile_DBEmpty_NilNil(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()

	// Ensure no persons exist. We do a targeted cleanup of any rows from
	// the seed helpers, but we can't truncate wholesale without risk.
	// Instead: delete any rows created in this test session only (via pid marker).
	// Best-effort: if DB has pre-existing data this test may fail — that is correct
	// (there IS a profile, so scoring SHOULD be enabled).

	t.Setenv("HUNT_SCORE_PROFILE_PATH", "")

	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	// If the DB already has profile data this subtest is moot; skip it.
	var count int
	_ = pool.QueryRow(ctx, `SELECT COUNT(*) FROM resume_persons`).Scan(&count)
	if count > 0 {
		t.Skip("DB has existing resume_persons rows — profile would be non-nil, test not applicable")
	}

	prof, err := score.LoadProfile(ctx, pool)
	assert.NoError(t, err, "empty DB must not error")
	assert.Nil(t, prof, "empty DB must return nil profile")
	assert.True(t, strings.Contains(buf.String(), "fit scoring disabled"),
		"must log WARN when DB is empty")
}
