package hunt_test

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
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// schemaFileNames lists every .sql file embedded under internal/hunt/schema
// in lexical order (the order pgutil applies them). 000_schema_versions.sql
// was removed when the store moved to pgutil, so this list starts at 001.
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

// openEphemeralTestDB creates a fresh, uniquely-named *_test database on the
// same server as DATABASE_URL and returns a pool connected to it. The DB is
// dropped on test cleanup. This gives T1/T2 the isolated, controlled starting
// state they need (a truly empty DB for T1; a hand-seeded legacy-adopted DB
// for T2) that the shared gojob_test harness cannot provide.
//
// The guard from dbtest.RequireTestDB still applies: DATABASE_URL must point
// at a *_test database (proving we're on a test server, not prod) before we
// create anything.
func openEphemeralTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	dbtest.RequireTestDB(t, dsn)

	// Random suffix keeps parallel test runs from colliding on the same name.
	var buf [8]byte
	_, err := rand.Read(buf[:])
	require.NoError(t, err, "read random suffix")
	ephName := "gojob_pgutil_" + hex.EncodeToString(buf[:]) + "_test"

	// Parse once; we derive two configs (maintenance → postgres DB, and the
	// ephemeral DB) by mutating ConnConfig.Database. We use NewWithConfig
	// rather than re-serializing via ConnString() because ConnString() returns
	// the cached parse-time string and does NOT reflect the mutated Database
	// field — pgxpool.New would silently connect to the original DB.
	baseCfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err, "parse DATABASE_URL")

	maintCfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	maintCfg.ConnConfig.Database = "postgres"

	maintPool, err := pgxpool.NewWithConfig(context.Background(), maintCfg)
	require.NoError(t, err, "open maintenance pool")
	_, err = maintPool.Exec(context.Background(), `CREATE DATABASE `+ephName)
	maintPool.Close()
	require.NoError(t, err, "create ephemeral DB %s", ephName)

	// Connect to the new ephemeral DB.
	ephCfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	ephCfg.ConnConfig.Database = ephName
	ephPool, err := pgxpool.NewWithConfig(context.Background(), ephCfg)
	require.NoError(t, err, "open ephemeral pool")

	t.Cleanup(func() {
		ephPool.Close()
		// Reconnect to maintenance to drop the ephemeral DB. Errors are
		// non-fatal on cleanup but logged via t.Errorf so leaks surface.
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
	_ = baseCfg // baseCfg kept for clarity; maintCfg/ephCfg are independent parses
	return ephPool
}

// schemaMigrationsRows returns the set of names tracked in schema_migrations
// on the given pool, ordered by name.
func schemaMigrationsRows(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT name FROM schema_migrations ORDER BY name`)
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

// TestStore_Migrate_PgUtil_FreshDB (T1) verifies the fresh-DB path: on an
// empty database Baseline returns false, pgutil runs every .sql file in
// order, and schema_migrations ends up populated with every current filename.
//
// RED-on-revert: if Store.Migrate is reverted to the old schema_versions
// tracker, schema_migrations never gets created → this test errors on the
// first query. If Baseline is wired backwards (returns true on a fresh DB)
// no files run → app tables missing → the table-existence assertions fail.
func TestStore_Migrate_PgUtil_FreshDB(t *testing.T) {
	pool := openEphemeralTestDB(t)
	ctx := context.Background()

	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx), "fresh migrate must succeed")

	// schema_migrations must contain exactly the embedded .sql filenames.
	got := schemaMigrationsRows(t, pool)
	want := schemaFileNames(t)
	assert.Equal(t, want, got, "schema_migrations must list every embedded .sql file")

	// A representative app table from the first migration must exist.
	var exists bool
	err := pool.QueryRow(ctx,
		`SELECT to_regclass('public.hunt_bounties') IS NOT NULL`).Scan(&exists)
	require.NoError(t, err)
	assert.True(t, exists, "hunt_bounties must exist after fresh migrate")

	// Re-running must be idempotent (pgutil skips applied files by checksum).
	require.NoError(t, s.Migrate(ctx), "second migrate must be idempotent")
	assert.Equal(t, want, schemaMigrationsRows(t, pool),
		"schema_migrations unchanged after idempotent re-run")
}

// TestStore_Migrate_PgUtil_AdoptedDB (T2) is the CRITICAL continuity test: it
// seeds a database that mimics an existing prod deploy brought up by the old
// schema_versions tracker, then calls the NEW Store.Migrate and asserts that
// the Baseline path is taken — every current file is marked applied in
// schema_migrations WITHOUT any migration re-running (a sentinel row seeded
// before Migrate survives untouched, and the legacy schema_versions rows are
// preserved).
//
// RED-on-revert: if Store.Migrate is reverted to the old tracker, it would
// re-run migrations against the already-created tables (no-op via IF NOT
// EXISTS) but schema_migrations would never be populated → the
// schema_migrations assertion fails. If Baseline is removed/wrong (hardwired
// to return false), pgutil takes the apply-path and re-runs every file. The
// migrations are idempotent (CREATE/ALTER … IF NOT EXISTS), so the
// tracking-table, sentinel-row, and legacy-schema_versions assertions all
// hold identically under either path — they do NOT discriminate baseline
// from apply. The drop-column probe below DOES: 007_hunt_status_columns.sql
// adds hunt_bounties.closed_at via ADD COLUMN IF NOT EXISTS; we drop that
// column post-seed and assert it stays absent after Migrate. Baseline marks
// 007 applied without executing it → closed_at stays dropped. A broken
// Baseline (return false) re-runs 007 → ADD COLUMN IF NOT EXISTS re-adds
// closed_at → the probe FAILS. This mirrors the oversize T2 discriminator.
func TestStore_Migrate_PgUtil_AdoptedDB(t *testing.T) {
	pool := openEphemeralTestDB(t)
	ctx := context.Background()

	// --- Seed a legacy-adopted DB: old tracker + app tables + a sentinel row.
	// 1. Create the OLD schema_versions tracker (the shape the pre-pgutil
	//    code used: version TEXT PK, applied_at).
	_, err := pool.Exec(ctx, `
		CREATE TABLE schema_versions (
			version     TEXT PRIMARY KEY,
			applied_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`)
	require.NoError(t, err, "seed legacy schema_versions")

	// 2. Run every current .sql file manually to stand up the app tables
	//    (simulating what the old Migrate did). 000_schema_versions.sql is
	//    gone from the embed set, so we only run 001+.
	files := schemaFileNames(t)
	for _, name := range files {
		data, rerr := os.ReadFile(filepath.Join("schema", name))
		require.NoError(t, rerr, "read %s for seed", name)
		_, eerr := pool.Exec(ctx, string(data))
		require.NoError(t, eerr, "seed: exec %s", name)
		// Record under the OLD tracker exactly as the old code did.
		_, ierr := pool.Exec(ctx,
			`INSERT INTO schema_versions (version) VALUES ($1) ON CONFLICT DO NOTHING`,
			name)
		require.NoError(t, ierr, "seed: record %s in schema_versions", name)
	}
	// The old code also recorded 000_schema_versions.sql; mimic that so the
	// legacy tracker looks like a real pre-pgutil deploy.
	_, err = pool.Exec(ctx,
		`INSERT INTO schema_versions (version) VALUES ($1) ON CONFLICT DO NOTHING`,
		"000_schema_versions.sql")
	require.NoError(t, err, "seed: record 000 in schema_versions")

	// 3. Sentinel row in hunt_bounties — must survive the new Migrate untouched.
	sentinelDedup := "pgutil-adopted-sentinel-xyz"
	_, err = pool.Exec(ctx, `
		INSERT INTO hunt_bounties (dedup_hash, title, url, source)
		VALUES ($1, 'sentinel', 'https://sentinel.example/adopted', 'test')`,
		sentinelDedup)
	require.NoError(t, err, "seed sentinel bounty")

	// 4. Drop a column that 007_hunt_status_columns.sql would (re-)add via
	//    ADD COLUMN IF NOT EXISTS. The app table still exists so Baseline's
	//    to_regclass probe returns true and the pre-existing state still
	//    looks "adopted" (every current file is recorded as applied by the
	//    seed). After Migrate, closed_at MUST still be absent: Baseline marks
	//    files applied without executing them, so 007's ADD COLUMN IF NOT
	//    EXISTS never runs. If Baseline were broken (return false), apply-path
	//    would re-run 007 and re-add closed_at → the probe below fails. The
	//    idempotent IF NOT EXISTS makes the column's absence the only
	//    observable difference between the two paths (the tracking-table,
	//    sentinel, and legacy-tracker assertions hold identically under
	//    either path because the migrations are idempotent).
	_, err = pool.Exec(ctx,
		`ALTER TABLE hunt_bounties DROP COLUMN closed_at`)
	require.NoError(t, err, "drop closed_at post-seed to set up the discriminator")

	// --- Call the NEW Migrate. Baseline must fire (schema_versions exists +
	// non-empty) → all files marked applied, none executed.
	s := hunt.NewStore(pool)
	require.NoError(t, s.Migrate(ctx), "adopted migrate must succeed without re-running DDL")

	// DISCRIMINATOR: closed_at must STILL be absent — proves 007's ADD COLUMN
	// did not execute → Baseline path was taken. A broken Baseline (return
	// false) would re-add it and this assertion would FAIL.
	var closedAtPresent bool
	err = pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = 'hunt_bounties'
			  AND column_name = 'closed_at'
		)`).Scan(&closedAtPresent)
	require.NoError(t, err, "probe information_schema for closed_at")
	assert.False(t, closedAtPresent,
		"closed_at must remain absent: Baseline must NOT have re-run 007 (apply-path would re-add it)")

	// schema_migrations must now list every current file (001+, no 000).
	got := schemaMigrationsRows(t, pool)
	assert.Equal(t, files, got,
		"schema_migrations must list every current .sql file after adoption")

	// The sentinel row must survive — proves no migration re-ran destructively.
	var sentinelExists bool
	err = pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM hunt_bounties WHERE dedup_hash = $1)`,
		sentinelDedup).Scan(&sentinelExists)
	require.NoError(t, err)
	assert.True(t, sentinelExists,
		"sentinel row must survive adoption — a re-run migration would have touched hunt_bounties")

	// The legacy schema_versions table must be left intact (orphaned, not
	// dropped, not rewritten) — confirms pgutil did not touch the old tracker.
	var legacyCount int
	err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM schema_versions`).Scan(&legacyCount)
	require.NoError(t, err)
	assert.Equal(t, len(files)+1, legacyCount,
		"legacy schema_versions rows must be preserved (orphaned, untouched)")

	// Re-running must be idempotent on the now-adopted DB.
	require.NoError(t, s.Migrate(ctx), "second migrate on adopted DB must be idempotent")
	assert.Equal(t, files, schemaMigrationsRows(t, pool),
		"schema_migrations unchanged after idempotent re-run on adopted DB")
}
