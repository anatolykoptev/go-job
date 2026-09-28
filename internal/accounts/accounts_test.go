package accounts_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/anatolykoptev/go-panel/auth"
	"github.com/anatolykoptev/go_job/internal/accounts"
	"github.com/anatolykoptev/go_job/internal/dbtest"
	"github.com/google/uuid"
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

// TestBootstrap_RoleConstraint proves the ADR-2 contract on a real database:
// a role-omitting INSERT fails (default dropped — every account is provisioned
// deliberately), an 'owner' INSERT fails (the CHECK keeps RequireRole's
// super-bypass unreachable), 'user'/'admin' insert fine, and a second Bootstrap
// is a no-op.
func TestBootstrap_RoleConstraint(t *testing.T) {
	pool := openTestPool(t)
	dbtest.DropAccountTables(t, pool)
	ctx := context.Background()

	_, _, err := accounts.Bootstrap(ctx, pool, accounts.OperatorSeed{})
	require.NoError(t, err)

	// Role-omitting insert must fail — the column default is dropped.
	_, err = pool.Exec(ctx,
		`INSERT INTO panel_accounts (email, name, password_hash) VALUES ('norole@t.example','x','h')`)
	require.Error(t, err, "role-omitting insert must fail after DROP DEFAULT")

	// 'owner' is unwritable — RequireRole/HasRole's super-bypass stays dead.
	_, err = pool.Exec(ctx,
		`INSERT INTO panel_accounts (email, name, role, password_hash) VALUES ('owner@t.example','x','owner','h')`)
	require.Error(t, err, "role='owner' insert must be rejected by CHECK")

	for _, role := range []string{"user", "admin"} {
		_, err = pool.Exec(ctx,
			`INSERT INTO panel_accounts (email, name, role, password_hash) VALUES ($1,'x',$2,'h')`,
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
	dbtest.DropAccountTables(t, pool)
	ctx := context.Background()

	_, _, err := accounts.Bootstrap(ctx, pool, accounts.OperatorSeed{})
	require.NoError(t, err)

	// Reconstruct the pre-migration shape: default present, constraint absent.
	_, err = pool.Exec(ctx, `
		ALTER TABLE panel_accounts DROP CONSTRAINT panel_accounts_role_check;
		ALTER TABLE panel_accounts ALTER COLUMN role SET DEFAULT 'admin';
		INSERT INTO panel_accounts (email, name, role, password_hash) VALUES ('legacy-owner@t.example','x','owner','h')`)
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
	dbtest.DropAccountTables(t, pool)
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
// body, EnsureSchema must precede the role-migration Exec, which precedes the
// accounts-owned DDL (mcp_api_keys, then account_job_scores), which precedes
// seedOperator — the ALTER and both FKs touch tables EnsureSchema creates,
// and the seed writes through the constraint. A reorder that compiles is
// still broken; this test is the load-bearing-order witness.
func TestBootstrap_Order_SourceGate(t *testing.T) {
	src, err := os.ReadFile("accounts.go")
	require.NoError(t, err)
	s := string(src)

	ensure := strings.Index(s, "store.EnsureSchema(ctx)")
	migrate := strings.Index(s, "pool.Exec(ctx, roleMigrationSQL)")
	keys := strings.Index(s, "pool.Exec(ctx, mcpAPIKeysSchema)")
	scores := strings.Index(s, "pool.Exec(ctx, accountJobScoresSchema)")
	settings := strings.Index(s, "pool.Exec(ctx, accountHuntSettingsSchema)")
	ratingsScope := strings.Index(s, "EnsureHuntRatingsAccountScope(ctx")
	resumeScope := strings.Index(s, "EnsureResumeAccountScope(ctx")
	oversizeScope := strings.Index(s, "EnsureOversizeAccountScope(ctx")
	seed := strings.Index(s, "seedOperator(ctx")
	backfillScores := strings.Index(s, "BackfillLegacyJobScores(ctx")
	backfillRatings := strings.Index(s, "BackfillHuntRatingsAccount(ctx")
	backfillSettings := strings.Index(s, "BackfillLegacyHuntSettings(ctx")
	backfillResume := strings.Index(s, "BackfillResumeAccountData(ctx")
	constrain := strings.Index(s, "ConstrainAccountColumns(ctx")
	require.Positive(t, ensure, "EnsureSchema call site missing from Bootstrap")
	require.Positive(t, migrate, "roleMigrationSQL Exec missing from Bootstrap")
	require.Positive(t, keys, "mcpAPIKeysSchema Exec missing from Bootstrap")
	require.Positive(t, scores, "accountJobScoresSchema Exec missing from Bootstrap")
	require.Positive(t, settings, "accountHuntSettingsSchema Exec missing from Bootstrap")
	require.Positive(t, ratingsScope, "EnsureHuntRatingsAccountScope call missing from Bootstrap")
	require.Positive(t, seed, "seedOperator call missing from Bootstrap")
	require.Positive(t, backfillScores, "BackfillLegacyJobScores call missing from Bootstrap")
	require.Positive(t, backfillRatings, "BackfillHuntRatingsAccount call missing from Bootstrap")
	require.Positive(t, backfillSettings, "BackfillLegacyHuntSettings call missing from Bootstrap")
	require.Positive(t, resumeScope, "EnsureResumeAccountScope call missing from Bootstrap")
	require.Positive(t, oversizeScope, "EnsureOversizeAccountScope call missing from Bootstrap")
	require.Positive(t, backfillResume, "BackfillResumeAccountData call missing from Bootstrap")
	require.Positive(t, constrain, "ConstrainAccountColumns call missing from Bootstrap")
	require.Less(t, ensure, migrate, "ADR-6: EnsureSchema must precede the role migration")
	require.Less(t, migrate, keys, "accounts-owned mcp_api_keys DDL applies after the role migration")
	require.Less(t, keys, scores, "accounts-owned account_job_scores DDL applies after mcp_api_keys")
	require.Less(t, scores, settings, "accounts-owned account_hunt_settings DDL applies after account_job_scores")
	require.Less(t, settings, ratingsScope, "hunt_ratings account scope is re-ensured after the accounts-owned DDL")
	require.Less(t, ratingsScope, seed, "all schema lands before the operator seed")
	require.Less(t, ratingsScope, resumeScope, "P4 scope ensures land after the hunt scope ensure")
	require.Less(t, resumeScope, oversizeScope, "P4 scope ensures land after the hunt scope ensure")
	require.Less(t, oversizeScope, seed, "all schema lands before the operator seed")
	require.Less(t, seed, backfillScores, "backfills need the seeded operator UUID")
	require.Less(t, seed, backfillRatings, "backfills need the seeded operator UUID")
	require.Less(t, seed, backfillSettings, "backfills need the seeded operator UUID")
	require.Less(t, seed, backfillResume, "resume backfill needs the seeded operator UUID")
	require.Less(t, backfillScores, constrain, "P5 constrain runs after ALL backfills (ADR-13 data gate)")
	require.Less(t, backfillRatings, constrain, "P5 constrain runs after ALL backfills")
	require.Less(t, backfillSettings, constrain, "P5 constrain runs after ALL backfills")
	require.Less(t, backfillResume, constrain, "P5 constrain runs after ALL backfills")
}

// TestBootstrap_PrecedesHuntMigrate_SourceGate is the main.go half of the
// ADR-6 ordering contract: the bootstrapAccounts wrapper (the single
// accounts.Bootstrap call site) must be invoked on initEngine's DB-ready
// path BEFORE the hStore.Migrate runner — the accounts substrate lands ahead
// of every other schema consumer — and must not live inside
// startAdminServer: the MCP bearer verifier needs the account schema whether
// or not the admin UI ever initializes. (The account-FK mcp_api_keys table
// itself now migrates INSIDE Bootstrap — internal/accounts/mcp_api_keys.sql —
// so no standard-roots file may reference panel_accounts again.) A Bootstrap
// call moved back into the admin path (post-migrate, admin-only) or a second
// call site both fail this gate.
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
		"ADR-6: bootstrapAccounts must precede the hunt migration runner — the accounts substrate precedes all schema consumers")

	admin := body("startAdminServer")
	require.NotContains(t, admin, "accounts.Bootstrap(",
		"Bootstrap must not live inside startAdminServer — the account schema must exist even when the admin UI never starts")
	require.NotContains(t, admin, "bootstrapAccounts(",
		"bootstrapAccounts must not be called from the admin path — initEngine's DB block is the only legal call site")
}

// TestSetNotifyChatID_Upsert proves --notify-chat-id upserts: a pre-existing
// settings row takes the new chat id and keeps its enabled flag (the former
// ON CONFLICT DO NOTHING silently dropped updates to existing rows).
func TestSetNotifyChatID_Upsert(t *testing.T) {
	pool := openTestPool(t)
	dbtest.DropAccountTables(t, pool)
	ctx := context.Background()

	_, op, err := accounts.Bootstrap(ctx, pool,
		accounts.OperatorSeed{Email: "op-upsert@t.dev", Password: "pw-pw-pw-pw", Name: "Op"})
	require.NoError(t, err)
	require.NotNil(t, op)

	_, err = pool.Exec(ctx,
		`INSERT INTO account_hunt_settings (account_id, enabled, notify_chat_id) VALUES ($1, true, 111)
		 ON CONFLICT (account_id) DO UPDATE SET enabled = true, notify_chat_id = 111`,
		op.ID)
	require.NoError(t, err)

	stored, err := accounts.SetNotifyChatID(ctx, pool, uuid.MustParse(op.ID), -222)
	require.NoError(t, err)
	require.True(t, stored)

	var chatID int64
	var enabled bool
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT notify_chat_id, enabled FROM account_hunt_settings WHERE account_id = $1`,
		op.ID).Scan(&chatID, &enabled))
	require.Equal(t, int64(-222), chatID, "existing row must take the new chat id")
	require.True(t, enabled, "upsert must preserve the enabled flag")
}
