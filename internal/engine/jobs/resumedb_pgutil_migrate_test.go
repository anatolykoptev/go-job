package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/anatolykoptev/go_job/internal/dbtest"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// schemaFileNames lists every .sql file embedded under internal/engine/jobs/schema
// in lexical order (the order pgutil applies them).
func schemaFileNames(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir("schema")
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	require.NotEmpty(t, names, "schema dir must contain .sql files")
	return names
}

// softSchemaFileNames returns the subset of schema files whose first
// non-whitespace line is "-- soft" (pgutil's soft-migration marker).
func softSchemaFileNames(t *testing.T) []string {
	t.Helper()
	var soft []string
	for _, name := range schemaFileNames(t) {
		data, err := os.ReadFile(filepath.Join("schema", name))
		require.NoError(t, err, "read %s", name)
		if strings.HasPrefix(strings.TrimSpace(string(data)), "-- soft") {
			soft = append(soft, name)
		}
	}
	return soft
}

// openEphemeralTestDB creates a fresh, uniquely-named *_test database on the
// same server as DATABASE_URL and returns a pool connected to it. The DB is
// dropped on test cleanup. Mirrors the oversize_pgutil_migrate_test harness.
func openEphemeralTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	dbtest.RequireTestDB(t, dsn)

	var buf [8]byte
	_, err := rand.Read(buf[:])
	require.NoError(t, err, "read random suffix")
	ephName := "gojob_resumedb_pgutil_" + hex.EncodeToString(buf[:]) + "_test"

	maintCfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err, "parse DATABASE_URL")
	maintCfg.ConnConfig.Database = "postgres"

	maintPool, err := pgxpool.NewWithConfig(context.Background(), maintCfg)
	require.NoError(t, err, "open maintenance pool")
	_, err = maintPool.Exec(context.Background(), `CREATE DATABASE `+ephName)
	maintPool.Close()
	require.NoError(t, err, "create ephemeral DB %s", ephName)

	ephCfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	ephCfg.ConnConfig.Database = ephName
	ephPool, err := pgxpool.NewWithConfig(context.Background(), ephCfg)
	require.NoError(t, err, "open ephemeral pool")

	t.Cleanup(func() {
		ephPool.Close()
		dropPool, derr := pgxpool.NewWithConfig(context.Background(), maintCfg)
		if derr != nil {
			t.Errorf("drop ephemeral DB: open maintenance pool: %v", derr)
			return
		}
		if _, derr := dropPool.Exec(context.Background(), `DROP DATABASE IF EXISTS `+ephName); derr != nil {
			t.Errorf("drop ephemeral DB %s: %v", ephName, derr)
		}
		dropPool.Close()
	})
	return ephPool
}

// trackingTableRows returns the set of names tracked in resume_schema_migrations
// on the given pool, ordered by name.
func trackingTableRows(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT name FROM resume_schema_migrations ORDER BY name`)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		out = append(out, n)
	}
	require.NoError(t, rows.Err())
	return out
}

// extensionInstalled reports whether a Postgres extension is installed on the
// given pool (used to decide which soft-migration assertions are reachable:
// if AGE/pgvector is present the soft file applies and is recorded; if absent
// it soft-skips and is NOT recorded).
func extensionInstalled(t *testing.T, pool *pgxpool.Pool, name string) bool {
	t.Helper()
	var installed bool
	err := pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = $1)`, name,
	).Scan(&installed)
	require.NoError(t, err, "probe pg_extension for %s", name)
	return installed
}

// TestResumeDB_Migrate_PgUtil_FreshDB (T1) verifies the fresh-DB path: on an
// empty database pgutil runs every .sql file in order. Non-soft files are
// recorded in resume_schema_migrations; soft files (002 AGE, 005 pgvector)
// are recorded iff the extension they need is present on the CI Postgres,
// otherwise they soft-skip (warn + continue + not recorded + retried next
// run). The run must succeed and be idempotent on re-run.
//
// RED-on-revert: if runMigrations is reverted to the old no-tracker loop,
// resume_schema_migrations never gets created → the tracking-table query
// errors on the first assertion.
func TestResumeDB_Migrate_PgUtil_FreshDB(t *testing.T) {
	pool := openEphemeralTestDB(t)
	ctx := context.Background()

	db := &ResumeDB{pool: pool}
	require.NoError(t, db.runMigrations(ctx), "fresh migrate must succeed")

	got := trackingTableRows(t, pool)

	// Expected tracked set = every non-soft file + every soft file whose
	// extension is installed on this Postgres.
	soft := softSchemaFileNames(t)
	softInstalled := map[string]bool{}
	for _, name := range soft {
		switch name {
		case "002_resume_graph.sql":
			softInstalled[name] = extensionInstalled(t, pool, "age")
		case "005_resume_vectors_embedding.sql":
			softInstalled[name] = extensionInstalled(t, pool, "vector")
		case "007_resume_vectors_source_backfill.sql":
			// One-shot data backfill marked -- soft to pass the idempotency
			// guard; it has no extension gate and always applies.
			softInstalled[name] = true
		case "008_resume_master_variant.sql":
			// One-shot data backfill (DO $$ block) marked -- soft to pass
			// the idempotency guard; no extension gate, always applies.
			softInstalled[name] = true
		}
	}
	var want []string
	for _, name := range schemaFileNames(t) {
		if strings.HasPrefix(strings.TrimSpace(string(mustReadSchema(t, name))), "-- soft") {
			if softInstalled[name] {
				want = append(want, name)
			}
			continue
		}
		want = append(want, name)
	}
	sort.Strings(want)
	assert.Equal(t, want, got, "resume_schema_migrations must list every applied file (soft files only when their extension is present)")

	// A core app table must exist regardless of soft outcomes.
	var personsExists bool
	err := pool.QueryRow(ctx,
		`SELECT to_regclass('public.resume_persons') IS NOT NULL`).Scan(&personsExists)
	require.NoError(t, err)
	assert.True(t, personsExists, "resume_persons must exist after fresh migrate")

	// Re-running must be idempotent (pgutil skips applied files by checksum;
	// soft files that soft-skipped are retried but remain unrecorded if the
	// extension is still absent, so the tracked set is stable).
	require.NoError(t, db.runMigrations(ctx), "second migrate must be idempotent")
	assert.Equal(t, want, trackingTableRows(t, pool),
		"resume_schema_migrations unchanged after idempotent re-run")
}

// TestResumeDB_Migrate_PgUtil_AdoptedDB (T2) is the CRITICAL continuity test:
// it seeds a database that mimics an existing prod deploy brought up by the
// old no-tracker runner (which re-applied every file on every boot), then
// calls the NEW runMigrations and asserts NO data loss — a sentinel row
// seeded before Migrate survives untouched, the run does not abort, and
// re-running is idempotent.
//
// No Baseline is set for resumedb (unlike hunt/oversize): the old runner had
// no tracking table, so the new runner's first call re-applies every file.
// This is safe because every non-soft file is idempotent (IF NOT EXISTS /
// ADD COLUMN IF NOT EXISTS), and the soft files either no-op (extension
// present) or soft-skip (extension absent). The sentinel row proves no
// destructive re-run occurred.
//
// RED-on-revert: if runMigrations is reverted to the old no-tracker loop,
// resume_schema_migrations never gets created → the tracking-table assertion
// fails.
func TestResumeDB_Migrate_PgUtil_AdoptedDB(t *testing.T) {
	pool := openEphemeralTestDB(t)
	ctx := context.Background()

	// --- Seed a legacy-adopted DB: app tables + a sentinel row.
	// The old runner had no tracker; it re-ran every .sql file every boot.
	// Simulate that by running every current file manually. Soft files may
	// fail here if the extension is absent — that matches the old warn-and-
	// continue behaviour; ignore soft failures during seeding.
	for _, name := range schemaFileNames(t) {
		data, rerr := os.ReadFile(filepath.Join("schema", name))
		require.NoError(t, rerr, "read %s for seed", name)
		_, eerr := pool.Exec(ctx, string(data))
		if eerr != nil && strings.HasPrefix(strings.TrimSpace(string(data)), "-- soft") {
			// Soft seed failure (extension absent) — expected on CI without
			// AGE/pgvector. Reset search_path and continue, matching the old
			// runner's post-soft reset.
			_, _ = pool.Exec(ctx, "SET search_path TO public")
			continue
		}
		require.NoError(t, eerr, "seed: exec %s", name)
	}

	// Sentinel row in resume_persons — must survive the new runMigrations.
	sentinelName := "pgutil-adopted-sentinel-resumedb"
	var sentinelID int
	err := pool.QueryRow(ctx, `
		INSERT INTO resume_persons (name, email, summary)
		VALUES ($1, 'sentinel@example.com', 'sentinel')
		RETURNING id`, sentinelName).Scan(&sentinelID)
	require.NoError(t, err, "seed sentinel resume_persons row")

	// --- Call the NEW runMigrations. Must succeed without re-running
	// destructive DDL (non-soft files are idempotent no-ops; soft files
	// either no-op or soft-skip).
	db := &ResumeDB{pool: pool}
	require.NoError(t, db.runMigrations(ctx), "adopted migrate must succeed without aborting")

	// The sentinel row must survive — proves no destructive re-run.
	var sentinelExists bool
	err = pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM resume_persons WHERE id = $1 AND name = $2)`,
		sentinelID, sentinelName).Scan(&sentinelExists)
	require.NoError(t, err)
	assert.True(t, sentinelExists,
		"sentinel row must survive adoption — a destructive re-run would have touched resume_persons")

	// resume_schema_migrations must now list every non-soft file + every soft
	// file whose extension is present (same expectation as T1, since the
	// adopted DB has the same extensions as a fresh one on the same server).
	got := trackingTableRows(t, pool)
	soft := softSchemaFileNames(t)
	softInstalled := map[string]bool{}
	for _, name := range soft {
		switch name {
		case "002_resume_graph.sql":
			softInstalled[name] = extensionInstalled(t, pool, "age")
		case "005_resume_vectors_embedding.sql":
			softInstalled[name] = extensionInstalled(t, pool, "vector")
		case "007_resume_vectors_source_backfill.sql":
			// One-shot data backfill marked -- soft to pass the idempotency
			// guard; it has no extension gate and always applies.
			softInstalled[name] = true
		case "008_resume_master_variant.sql":
			// One-shot data backfill (DO $$ block) marked -- soft to pass
			// the idempotency guard; no extension gate, always applies.
			softInstalled[name] = true
		}
	}
	var want []string
	for _, name := range schemaFileNames(t) {
		if strings.HasPrefix(strings.TrimSpace(string(mustReadSchema(t, name))), "-- soft") {
			if softInstalled[name] {
				want = append(want, name)
			}
			continue
		}
		want = append(want, name)
	}
	sort.Strings(want)
	assert.Equal(t, want, got,
		"resume_schema_migrations must list every applied file after adoption")

	// Re-running must be idempotent on the now-adopted DB.
	require.NoError(t, db.runMigrations(ctx), "second migrate on adopted DB must be idempotent")
	assert.Equal(t, want, trackingTableRows(t, pool),
		"resume_schema_migrations unchanged after idempotent re-run on adopted DB")
}

// TestResumeDB_Migrate_PgUtil_SoftContinues (T3) confirms that a soft
// migration whose extension is ABSENT does NOT abort the run: the run
// completes, non-soft tables exist, and the soft file is NOT recorded (so it
// retries on the next run). CI's preflight Postgres (pgvector/pgvector:pg16)
// has pgvector but NOT Apache AGE, so 002_resume_graph.sql naturally
// exercises the soft-skip path. If AGE happens to be installed on the test
// server, this test soft-skips with a clear message (the soft-continue path
// is not reachable there).
//
// RED-on-revert: if runMigrations is reverted to the old no-tracker loop AND
// the soft detection were removed, a soft failure would abort the run →
// resume_persons would not exist → the table-existence assertion fails. With
// the old soft detection intact the run completes, but
// resume_schema_migrations is never created → the tracking-table assertion
// fails.
func TestResumeDB_Migrate_PgUtil_SoftContinues(t *testing.T) {
	pool := openEphemeralTestDB(t)
	ctx := context.Background()

	if extensionInstalled(t, pool, "age") {
		t.Skip("AGE extension is installed on this Postgres — the soft-skip path for 002_resume_graph.sql is not reachable; skipping T3")
	}

	db := &ResumeDB{pool: pool}
	require.NoError(t, db.runMigrations(ctx), "migrate must NOT abort when a soft migration's extension is absent")

	// The run completed despite 002's soft failure → later non-soft tables
	// (006_upwork_profile.sql creates upwork_profile) must exist.
	var upworkProfileExists bool
	err := pool.QueryRow(ctx,
		`SELECT to_regclass('public.upwork_profile') IS NOT NULL`).Scan(&upworkProfileExists)
	require.NoError(t, err)
	assert.True(t, upworkProfileExists,
		"upwork_profile must exist — proves the run continued past 002's soft failure to apply 006")

	// 002 must NOT be recorded (soft-skip → not applied → retries next run).
	tracked := trackingTableRows(t, pool)
	assert.NotContains(t, tracked, "002_resume_graph.sql",
		"002_resume_graph.sql must NOT be recorded when AGE is absent — soft-skip leaves it unrecorded so it retries")

	// A later non-soft file (006) MUST be recorded — proves the run continued
	// and recorded subsequent successful files, not just bailed silently.
	assert.Contains(t, tracked, "006_upwork_profile.sql",
		"006_upwork_profile.sql must be recorded — proves the run continued past the soft failure and recorded later files")
}

// mustReadSchema reads a schema file for soft-detection checks in assertions.
func mustReadSchema(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("schema", name))
	require.NoError(t, err, "read %s", name)
	return data
}
