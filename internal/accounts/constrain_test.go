package accounts_test

// constrain_test.go — the P5 data gate's proof (plan ADR-13): Bootstrap's
// constrain step SETs NOT NULL on every expand-half account_id once zero
// NULLs remain, refuses the boot loudly when they don't, and is a no-op on
// the second run.
//
// RED-on-revert: deleting the ConstrainAccountColumns call from Bootstrap
// leaves every column nullable — the is_nullable assertions fail. Skipping
// the dirty check makes the ALTER die mid-migration on a constraint error
// instead of the loud per-table refusal — the dirty test fails on message.

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

// constrainTablesUnderTest lists the expand-half tables P5 constrains —
// mirrors constrainTables in internal/accounts/constrain.go (kept local so a
// drift between the two is loud, not silently consistent).
var constrainTablesUnderTest = []string{
	"hunt_ratings",
	"resume_persons",
	"resume_vectors",
	"oversize_responses",
}

// TestBootstrap_ConstrainSetsNotNull is the end-to-end constrain gate:
// DropAccountTables → Bootstrap → migrate every schema root → second
// Bootstrap. The second run is the fresh-DB ordering (tables created after
// the first Bootstrap get constrained on the next) AND the idempotency
// proof for everything else.
func TestBootstrap_ConstrainSetsNotNull(t *testing.T) {
	pool := openTestPool(t)
	dbtest.DropAccountTables(t, pool)
	ctx := context.Background()

	_, _, err := accounts.Bootstrap(ctx, pool, accounts.OperatorSeed{})
	require.NoError(t, err, "first Bootstrap")

	// Migrate the schema roots that create the late-bound account tables —
	// the same order initEngine runs (Bootstrap precedes hStore.Migrate).
	require.NoError(t, hunt.NewStore(pool).Migrate(ctx), "hunt.Migrate")
	rdb, err := jobs.ConnectResumeDB(ctx, os.Getenv("DATABASE_URL"))
	require.NoError(t, err, "ConnectResumeDB")
	defer rdb.Close()
	require.NoError(t, oversize.NewStore(pool).Migrate(ctx), "oversize.Migrate")

	_, _, err = accounts.Bootstrap(ctx, pool, accounts.OperatorSeed{})
	require.NoError(t, err, "second Bootstrap must be a clean idempotent no-op")

	// Every expand-half account_id is now NOT NULL.
	for _, tbl := range constrainTablesUnderTest {
		var nullable string
		require.NoError(t, pool.QueryRow(ctx, `
			SELECT is_nullable FROM information_schema.columns
			WHERE table_schema = current_schema()
			  AND table_name = $1 AND column_name = 'account_id'`, tbl).Scan(&nullable))
		assert.Equal(t, "NO", nullable, "%s.account_id must be NOT NULL after constrain", tbl)
	}

	// The account FKs are present unconditionally (the #494 conditional gap
	// is closed at Bootstrap time) and the UNIQUE scope swaps hold.
	for tbl, fk := range map[string]string{
		"hunt_ratings":       "hunt_ratings_account_id_fkey",
		"resume_persons":     "resume_persons_account_id_fkey",
		"resume_vectors":     "resume_vectors_account_id_fkey",
		"oversize_responses": "oversize_responses_account_id_fkey",
	} {
		var ok bool
		require.NoError(t, pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conname = $1 AND conrelid = $2::regclass AND contype = 'f'
			)`, fk, tbl).Scan(&ok))
		assert.True(t, ok, "FK %s on %s must exist", fk, tbl)
	}
	for _, uq := range []string{
		"hunt_ratings_entry_account_key",
		"resume_vectors_account_content_key",
	} {
		var ok bool
		require.NoError(t, pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_constraint WHERE conname = $1 AND contype = 'u'
			)`, uq).Scan(&ok))
		assert.True(t, ok, "account-scoped UNIQUE %s must exist", uq)
	}
	for _, uq := range []string{
		"hunt_ratings_entry_kind_entry_id_user_name_key",
		"resume_vectors_user_name_content_hash_key",
	} {
		var ok bool
		require.NoError(t, pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_constraint WHERE conname = $1 AND contype = 'u'
			)`, uq).Scan(&ok))
		assert.False(t, ok, "legacy user_name UNIQUE %s must be gone", uq)
	}
}

// TestBootstrap_ConstrainRefusesDirtyData is the data gate itself: a table
// carrying NULL account_id rows makes Bootstrap FAIL with a loud per-table
// count, and the column is left nullable — nothing constrains half-way.
func TestBootstrap_ConstrainRefusesDirtyData(t *testing.T) {
	pool := openTestPool(t)
	dbtest.DropAccountTables(t, pool)
	ctx := context.Background()

	_, _, err := accounts.Bootstrap(ctx, pool, accounts.OperatorSeed{})
	require.NoError(t, err)

	// Reconstruct the pre-constrain shape on hunt_ratings and plant an
	// ownerless row — exactly the dirty state the gate exists for.
	_, err = pool.Exec(ctx, `
		ALTER TABLE hunt_ratings ALTER COLUMN account_id DROP NOT NULL;
		INSERT INTO hunt_ratings (entry_kind, entry_id, account_id)
		VALUES ('job', -424242, NULL)`)
	require.NoError(t, err)
	t.Cleanup(func() {
		// Restore the constrained state for the rest of the suite.
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM hunt_ratings WHERE entry_id = -424242`)
		_, _, _ = accounts.Bootstrap(context.Background(), pool, accounts.OperatorSeed{})
	})

	_, _, err = accounts.Bootstrap(ctx, pool, accounts.OperatorSeed{})
	require.Error(t, err, "Bootstrap must refuse to constrain dirty data")
	assert.Contains(t, err.Error(), "constrain refused")
	assert.Contains(t, err.Error(), "hunt_ratings=1",
		"the refusal must name the table and its NULL count")

	// The refused gate changes nothing: the column stays nullable.
	var nullable string
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT is_nullable FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = 'hunt_ratings' AND column_name = 'account_id'`).Scan(&nullable))
	assert.Equal(t, "YES", nullable,
		"a refused constrain must leave the column nullable")
}
