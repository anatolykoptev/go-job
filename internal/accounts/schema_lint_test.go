package accounts_test

// schema_lint_test.go — the ADR-15 per-account vs shared-corpus
// classification, derived from LIVE information_schema rather than a
// handwritten copy of the DDL. The classification rule is structural:
//   - every account-namespaced table (account_*, mcp_*, panel_totp_*)
//     MUST carry account_id
//   - every shared-corpus table (hunt_*) MUST NOT carry account_id
//   - any other table that carries account_id without being declared in the
//     lint's account-owned set is unclassified — fail loudly so P3+ tables
//     update the classification when they land.
//
// RED-on-revert: create account_job_scores under internal/hunt/schema (or
// with the account predicate dropped) → the hunt_* namespace assertion or
// the explicit-membership assertion fails.

import (
	"context"
	"os"
	"testing"

	"github.com/anatolykoptev/go_job/internal/accounts"
	"github.com/anatolykoptev/go_job/internal/dbtest"
	jobs "github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/anatolykoptev/go_job/internal/oversize"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// accountOwnedTables is the declared ADR-15 per-account set — every table the
// schema lint expects to carry account_id. A new account-scoped table that
// lands without updating this set trips the "unclassified" assertion below.
var accountOwnedTables = map[string]bool{
	"account_hunt_settings":     true, // ADR-7 per-account worker knobs
	"account_job_scores":        true,
	"mcp_api_keys":              true,
	"panel_totp_recovery_codes": true,
	// hunt_ratings is account-owned (ADR-15) despite living in the hunt
	// schema — the corpus/… split is by ownership, not by namespace.
	"hunt_ratings": true,
	// P4 (ADR-8/9/10): resume hub + vectors and the oversize spill table
	// are account-owned; account_id lands via schema 008/003 plus the
	// Bootstrap Ensure* probes.
	"resume_persons":     true,
	"resume_vectors":     true,
	"oversize_responses": true,
}

func TestSchemaLint_AccountIDClassification(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()

	// Recreate the accounts schema from the absent-table state, then the
	// shared corpus schema — the same boot order main.go runs.
	dbtest.DropAccountTables(t, pool)
	_, _, err := accounts.Bootstrap(ctx, pool, accounts.OperatorSeed{})
	require.NoError(t, err, "accounts.Bootstrap")
	require.NoError(t, hunt.NewStore(pool).Migrate(ctx), "hunt.Migrate")
	// P4 tables must exist for the membership assertions — Bootstrap alone
	// no-ops on their absence (migrations precede panel_accounts only in a
	// cold boot). ConnectResumeDB applies the embedded schema; the soft
	// AGE/pgvector files degrade cleanly when the extensions are absent.
	rdb, err := jobs.ConnectResumeDB(ctx, os.Getenv("DATABASE_URL"))
	require.NoError(t, err, "ConnectResumeDB")
	defer rdb.Close()
	require.NoError(t, oversize.NewStore(pool).Migrate(ctx), "oversize.Migrate")
	// Re-run the account-scope probes post-migration — the ordering real
	// deploys hit when resume/oversize tables first appear.
	require.NoError(t, accounts.EnsureResumeAccountScope(ctx, pool))
	require.NoError(t, accounts.EnsureOversizeAccountScope(ctx, pool))

	// Derive the live classification: table → has account_id.
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT table_name
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND column_name = 'account_id'`)
	require.NoError(t, err)
	withAccountID := map[string]bool{}
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		withAccountID[name] = true
	}
	require.NoError(t, rows.Err())
	rows.Close()

	// The full live table set — needed to catch an unclassified account_id
	// carrier, not just to check the declared ones.
	allRows, err := pool.Query(ctx, `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'`)
	require.NoError(t, err)
	var allTables []string
	for allRows.Next() {
		var name string
		require.NoError(t, allRows.Scan(&name))
		allTables = append(allTables, name)
	}
	require.NoError(t, allRows.Err())
	allRows.Close()

	// 1. Every declared account-owned table exists and carries account_id.
	for tbl := range accountOwnedTables {
		assert.Contains(t, allTables, tbl, "%s must exist", tbl)
		assert.True(t, withAccountID[tbl], "%s must carry account_id (per-account class)", tbl)
	}

	// 2. Every shared-corpus hunt_* table lacks account_id — the corpus is
	//    account-blind by design (ADR-6/ADR-15). hunt_ratings is the declared
	//    exception: it holds per-account judgments, not corpus.
	for _, tbl := range allTables {
		if len(tbl) >= 5 && tbl[:5] == "hunt_" && !accountOwnedTables[tbl] {
			assert.False(t, withAccountID[tbl],
				"shared-corpus table %s must NOT carry account_id — per-account data lives in account_* tables", tbl)
		}
	}
	assert.Contains(t, allTables, "hunt_jobs", "shared corpus must be migrated")

	// 3. Namespace rule: every account_*/mcp_* table that exists must be in
	//    the declared set and carry account_id.
	for _, tbl := range allTables {
		if (len(tbl) >= 8 && tbl[:8] == "account_") || (len(tbl) >= 4 && tbl[:4] == "mcp_") {
			assert.True(t, accountOwnedTables[tbl], "table %s is unclassified — declare it in accountOwnedTables", tbl)
			assert.True(t, withAccountID[tbl], "%s must carry account_id", tbl)
		}
	}

	// 4. Reverse check: a table carrying account_id that is neither
	//    account-owned nor the identity root is unclassified — fail loudly.
	for tbl := range withAccountID {
		if !accountOwnedTables[tbl] && tbl != "panel_accounts" {
			assert.Fail(t, "table %s carries account_id but is not in the declared per-account set", tbl)
		}
	}

	// 5. account_hunt_settings is keyed BY the account (ADR-7): its PRIMARY
	//    KEY must be account_id alone — a surrogate key would re-open the
	//    multi-row shape the single-row hunt_settings table had. Derived from
	//    live information_schema, not the DDL text.
	var pkCols []string
	pkRows, err := pool.Query(ctx, `
		SELECT kcu.column_name
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
		  ON tc.constraint_name = kcu.constraint_name
		 AND tc.table_schema = kcu.table_schema
		 AND tc.table_name = kcu.table_name
		WHERE tc.table_schema = current_schema()
		  AND tc.table_name = 'account_hunt_settings'
		  AND tc.constraint_type = 'PRIMARY KEY'
		ORDER BY kcu.ordinal_position`)
	require.NoError(t, err)
	for pkRows.Next() {
		var col string
		require.NoError(t, pkRows.Scan(&col))
		pkCols = append(pkCols, col)
	}
	require.NoError(t, pkRows.Err())
	pkRows.Close()
	assert.Equal(t, []string{"account_id"}, pkCols,
		"account_hunt_settings PRIMARY KEY must be account_id — the row key IS the account")
}
