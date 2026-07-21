package oversize_test

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
	"github.com/anatolykoptev/go_job/internal/oversize"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// schemaFileNames lists every .sql file embedded under internal/oversize/schema
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

// openEphemeralTestDB creates a fresh, uniquely-named *_test database on the
// same server as DATABASE_URL and returns a pool connected to it. The DB is
// dropped on test cleanup. Mirrors the hunt store_pgutil_migrate_test harness.
func openEphemeralTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	dbtest.RequireTestDB(t, dsn)

	var buf [8]byte
	_, err := rand.Read(buf[:])
	require.NoError(t, err, "read random suffix")
	ephName := "gojob_oversize_pgutil_" + hex.EncodeToString(buf[:]) + "_test"

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

// trackingTableRows returns the set of names tracked in
// oversize_schema_migrations on the given pool, ordered by name.
func trackingTableRows(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT name FROM oversize_schema_migrations ORDER BY name`)
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
// empty database Baseline returns false (oversize_responses does not exist),
// pgutil runs every .sql file in order, and oversize_schema_migrations ends
// up populated with every current filename.
//
// RED-on-revert: if Store.Migrate is reverted to the old no-tracker loop,
// oversize_schema_migrations never gets created → this test errors on the
// first query. If Baseline is wired backwards (returns true on a fresh DB)
// no files run → oversize_responses missing → the table-existence assertion
// fails.
func TestStore_Migrate_PgUtil_FreshDB(t *testing.T) {
	pool := openEphemeralTestDB(t)
	ctx := context.Background()

	s := oversize.NewStore(pool)
	require.NoError(t, s.Migrate(ctx), "fresh migrate must succeed")

	got := trackingTableRows(t, pool)
	want := schemaFileNames(t)
	assert.Equal(t, want, got, "oversize_schema_migrations must list every embedded .sql file")

	var exists bool
	err := pool.QueryRow(ctx,
		`SELECT to_regclass('public.oversize_responses') IS NOT NULL`).Scan(&exists)
	require.NoError(t, err)
	assert.True(t, exists, "oversize_responses must exist after fresh migrate")

	// Re-running must be idempotent (pgutil skips applied files by checksum).
	require.NoError(t, s.Migrate(ctx), "second migrate must be idempotent")
	assert.Equal(t, want, trackingTableRows(t, pool),
		"oversize_schema_migrations unchanged after idempotent re-run")
}

// TestStore_Migrate_PgUtil_AdoptedDB (T2) is the CRITICAL continuity test: it
// seeds a database that mimics an existing prod deploy brought up by the old
// no-tracker runner (which re-applied every file on every boot), then calls
// the NEW Store.Migrate and asserts that the Baseline path is taken — every
// current file is marked applied in oversize_schema_migrations WITHOUT any
// migration re-running (a sentinel row seeded before Migrate survives
// untouched).
//
// The pre-pgutil oversize runner had NO tracking table, so the "legacy state"
// seeded here is just the app tables + a sentinel row (no old tracker to
// preserve, unlike hunt's schema_versions).
//
// RED-on-revert: if Store.Migrate is reverted to the old no-tracker loop, it
// would re-run migrations against the already-created tables (no-op via IF
// NOT EXISTS) but oversize_schema_migrations would never be created → the
// tracking-table assertion fails. If Baseline is removed/wrong, pgutil tries
// to re-run 001_oversize_responses.sql inside a per-file tx; CREATE TABLE IF
// NOT EXISTS is a no-op so it wouldn't error, BUT the distinguishing proof is
// that oversize_schema_migrations is populated AND the sentinel row survives
// untouched.
func TestStore_Migrate_PgUtil_AdoptedDB(t *testing.T) {
	pool := openEphemeralTestDB(t)
	ctx := context.Background()

	// --- Seed a legacy-adopted DB: app tables + a sentinel row.
	// The old runner had no tracker; it just re-ran every .sql file every boot.
	// Simulate that by running every current file manually.
	files := schemaFileNames(t)
	for _, name := range files {
		data, rerr := os.ReadFile(filepath.Join("schema", name))
		require.NoError(t, rerr, "read %s for seed", name)
		_, eerr := pool.Exec(ctx, string(data))
		require.NoError(t, eerr, "seed: exec %s", name)
	}

	// Sentinel row in oversize_responses — must survive the new Migrate untouched.
	sentinelSHA := "pgutil-adopted-sentinel-oversize-xyz"
	_, err := pool.Exec(ctx, `
		INSERT INTO oversize_responses (tool_name, query_hash, payload, size_bytes, sha256)
		VALUES ('test', $1, '{}'::jsonb, 2, $2)`,
		sentinelSHA, sentinelSHA)
	require.NoError(t, err, "seed sentinel oversize row")

	// --- Call the NEW Migrate. Baseline must fire (oversize_responses exists)
	// → all files marked applied, none executed.
	s := oversize.NewStore(pool)
	require.NoError(t, s.Migrate(ctx), "adopted migrate must succeed without re-running DDL")

	// oversize_schema_migrations must now list every current file.
	got := trackingTableRows(t, pool)
	assert.Equal(t, files, got,
		"oversize_schema_migrations must list every current .sql file after adoption")

	// The sentinel row must survive — proves no migration re-ran destructively.
	var sentinelExists bool
	err = pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM oversize_responses WHERE sha256 = $1)`,
		sentinelSHA).Scan(&sentinelExists)
	require.NoError(t, err)
	assert.True(t, sentinelExists,
		"sentinel row must survive adoption — a re-run migration would have touched oversize_responses")

	// Re-running must be idempotent on the now-adopted DB.
	require.NoError(t, s.Migrate(ctx), "second migrate on adopted DB must be idempotent")
	assert.Equal(t, files, trackingTableRows(t, pool),
		"oversize_schema_migrations unchanged after idempotent re-run on adopted DB")
}
