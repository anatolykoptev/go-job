package accounts_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/anatolykoptev/go-panel/auth"
	"github.com/anatolykoptev/go_job/internal/accounts"
	"github.com/anatolykoptev/go_job/internal/dbtest"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func openTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	dbtest.RequireTestDB(t, dsn)
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// dropAccountTables resets the account-schema state this suite owns. The
// tables are dropped (not truncated) so each test exercises EnsureSchema from
// the absent-table state — the same state Bootstrap faces on a fresh deploy.
// mcp_api_keys goes with them, and its schema_migrations tracking row is
// cleared: pgutil skips already-recorded files, so without the delete the
// next Migrate would leave mcp_api_keys dropped forever — a FK-less table a
// later test could silently verify against.
func dropAccountTables(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`DROP TABLE IF EXISTS mcp_api_keys;
		 DROP TABLE IF EXISTS panel_totp_recovery_codes;
		 DROP TABLE IF EXISTS panel_accounts;
		 DO $$
		 BEGIN
		     IF to_regclass('public.schema_migrations') IS NOT NULL THEN
		         DELETE FROM schema_migrations WHERE name = '014_mcp_api_keys.sql';
		     END IF;
		 END $$`)
	require.NoError(t, err)
}

// TestBootstrap_RoleConstraint proves the ADR-2 contract on a real database:
// a role-omitting INSERT fails (default dropped — every account is provisioned
// deliberately), an 'owner' INSERT fails (the CHECK keeps RequireRole's
// super-bypass unreachable), 'user'/'admin' insert fine, and a second Bootstrap
// is a no-op.
func TestBootstrap_RoleConstraint(t *testing.T) {
	pool := openTestPool(t)
	dropAccountTables(t, pool)
	ctx := context.Background()

	_, _, err := accounts.Bootstrap(ctx, pool, accounts.OperatorSeed{})
	require.NoError(t, err)

	// Role-omitting insert must fail — the column default is dropped.
	_, err = pool.Exec(ctx,
		`INSERT INTO panel_accounts (email, name) VALUES ('norole@t.example','x')`)
	require.Error(t, err, "role-omitting insert must fail after DROP DEFAULT")

	// 'owner' is unwritable — RequireRole/HasRole's super-bypass stays dead.
	_, err = pool.Exec(ctx,
		`INSERT INTO panel_accounts (email, name, role) VALUES ('owner@t.example','x','owner')`)
	require.Error(t, err, "role='owner' insert must be rejected by CHECK")

	for _, role := range []string{"user", "admin"} {
		_, err = pool.Exec(ctx,
			`INSERT INTO panel_accounts (email, name, role) VALUES ($1,'x',$2)`,
			"ok-"+role+"@t.example", role)
		require.NoError(t, err, "role=%s must insert", role)
	}

	var owners int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM panel_accounts WHERE role = 'owner'`).Scan(&owners))
	require.Zero(t, owners, "zero 'owner' rows — fitness invariant")

	// Constraint exists as a named object (the DO-block guard path).
	var constraintCount int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_constraint WHERE conname='panel_accounts_role_check'`).Scan(&constraintCount))
	require.Equal(t, 1, constraintCount)

	_, _, err = accounts.Bootstrap(ctx, pool, accounts.OperatorSeed{})
	require.NoError(t, err, "second Bootstrap must be idempotent")
}

// TestBootstrap_OwnerRowNormalized proves the migration's first step: a
// pre-constraint table carrying an 'owner' row is normalized to 'admin' BEFORE
// the CHECK is added (ADD CONSTRAINT would otherwise fail on the violation).
// Simulated by dropping the constraint + restoring the default on the live
// schema, inserting an owner row, then re-running Bootstrap.
func TestBootstrap_OwnerRowNormalized(t *testing.T) {
	pool := openTestPool(t)
	dropAccountTables(t, pool)
	ctx := context.Background()

	_, _, err := accounts.Bootstrap(ctx, pool, accounts.OperatorSeed{})
	require.NoError(t, err)

	// Reconstruct the pre-migration shape: default present, constraint absent.
	_, err = pool.Exec(ctx, `
		ALTER TABLE panel_accounts DROP CONSTRAINT panel_accounts_role_check;
		ALTER TABLE panel_accounts ALTER COLUMN role SET DEFAULT 'admin';
		INSERT INTO panel_accounts (email, name, role) VALUES ('legacy-owner@t.example','x','owner')`)
	require.NoError(t, err)

	_, _, err = accounts.Bootstrap(ctx, pool, accounts.OperatorSeed{})
	require.NoError(t, err, "Bootstrap must normalize, not fail on, a legacy owner row")

	var role string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT role FROM panel_accounts WHERE email='legacy-owner@t.example'`).Scan(&role))
	require.Equal(t, "admin", role)
}

// TestBootstrap_SeedOperator covers ADR-J: env-seeded operator admin, idempotent
// re-runs (CreateAccount ON CONFLICT DO NOTHING), and env-driven password
// rotation (UpdatePasswordHash re-syncs the hash every boot).
func TestBootstrap_SeedOperator(t *testing.T) {
	pool := openTestPool(t)
	dropAccountTables(t, pool)
	ctx := context.Background()
	seed := accounts.OperatorSeed{Email: "op@t.example", Password: "pw-one-pw-one", Name: "Op"}

	_, op, err := accounts.Bootstrap(ctx, pool, seed)
	require.NoError(t, err)
	require.NotNil(t, op)
	require.Equal(t, "op@t.example", op.Email)
	require.Equal(t, "admin", op.Role, "seeded operator is admin, never owner")
	require.True(t, op.Active)

	// Rotation: re-bootstrap with a new password — same account id, new hash.
	_, op2, err := accounts.Bootstrap(ctx, pool,
		accounts.OperatorSeed{Email: seed.Email, Password: "pw-two-pw-two"})
	require.NoError(t, err)
	require.Equal(t, op.ID, op2.ID, "rotation must not create a second account")

	acct, err := auth.NewPgxAccountStore(pool).GetByEmail(ctx, seed.Email)
	require.NoError(t, err)
	require.True(t, auth.VerifyPassword("pw-two-pw-two", acct.PasswordHash), "rotated password verifies")
	require.False(t, auth.VerifyPassword("pw-one-pw-one", acct.PasswordHash), "old password no longer verifies")

	// No seed envs → nil operator, no error, schema still provisioned.
	_, none, err := accounts.Bootstrap(ctx, pool, accounts.OperatorSeed{})
	require.NoError(t, err)
	require.Nil(t, none)
}

// TestBootstrap_Order_SourceGate is the ADR-6 boot-order gate: in Bootstrap's
// body, EnsureSchema must precede the role-migration Exec, which must precede
// seedOperator — the ALTER touches a table EnsureSchema creates, and the seed
// writes through the constraint. A reorder that compiles is still broken; this
// test is the load-bearing-order witness.
func TestBootstrap_Order_SourceGate(t *testing.T) {
	src, err := os.ReadFile("accounts.go")
	require.NoError(t, err)
	s := string(src)

	ensure := strings.Index(s, "store.EnsureSchema(ctx)")
	migrate := strings.Index(s, "pool.Exec(ctx, roleMigrationSQL)")
	seed := strings.Index(s, "seedOperator(ctx")
	require.Positive(t, ensure, "EnsureSchema call site missing from Bootstrap")
	require.Positive(t, migrate, "roleMigrationSQL Exec missing from Bootstrap")
	require.Positive(t, seed, "seedOperator call missing from Bootstrap")
	require.Less(t, ensure, migrate, "ADR-6: EnsureSchema must precede the role migration")
	require.Less(t, migrate, seed, "seed runs after the role constraint exists")
}

// TestBootstrap_PrecedesHuntMigrate_SourceGate is the main.go half of the
// ADR-6 ordering contract: the bootstrapAccounts wrapper (the single
// accounts.Bootstrap call site) must be invoked on initEngine's DB-ready
// path BEFORE the hStore.Migrate runner — a P1 standard-roots migration
// adding REFERENCES panel_accounts must find the table EnsureSchema
// created — and must not live inside startAdminServer: the MCP bearer
// verifier needs the account schema whether or not the admin UI ever
// initializes. A Bootstrap call moved back into the admin path (post-
// migrate, admin-only) or a second call site both fail this gate.
func TestBootstrap_PrecedesHuntMigrate_SourceGate(t *testing.T) {
	src, err := os.ReadFile("../../main.go")
	require.NoError(t, err)
	s := string(src)

	require.Equal(t, 1, strings.Count(s, "accounts.Bootstrap("),
		"exactly one accounts.Bootstrap call site must exist in main.go")
	require.Equal(t, 2, strings.Count(s, "bootstrapAccounts("),
		"bootstrapAccounts must have exactly one definition and one call site")

	body := func(name string) string {
		i := strings.Index(s, "func "+name+"(")
		require.Positive(t, i, name+" definition missing from main.go")
		rest := s[i:]
		if j := strings.Index(rest[1:], "\nfunc "); j >= 0 {
			return rest[:j+1]
		}
		return rest
	}

	init := body("initEngine")
	require.Contains(t, init, "bootstrapAccounts(",
		"initEngine must invoke bootstrapAccounts on its DB-ready path")
	boot := strings.Index(init, "bootstrapAccounts(")
	migrate := strings.Index(init, "hStore.Migrate(")
	require.Positive(t, migrate, "hStore.Migrate call missing from initEngine")
	require.Less(t, boot, migrate,
		"ADR-6: bootstrapAccounts must precede the hunt migration runner (P1 migrations may REFERENCES panel_accounts)")

	admin := body("startAdminServer")
	require.NotContains(t, admin, "accounts.Bootstrap(",
		"Bootstrap must not live inside startAdminServer — the account schema must exist even when the admin UI never starts")
	require.NotContains(t, admin, "bootstrapAccounts(",
		"bootstrapAccounts must not be called from the admin path — initEngine's DB block is the only legal call site")
}
