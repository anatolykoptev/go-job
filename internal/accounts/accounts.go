// Package accounts owns go-job's panel_accounts identity substrate: the boot
// sequence that provisions it (EnsureSchema, the go-job-owned role-constraint
// migration, the env-seeded operator admin) and the small identity types the
// auth-driver wiring in internal/adminui consumes.
//
// panel_accounts backs BOTH the admin UI's bcrypt+TOTP sessions
// (auth.BcryptTOTPAuth) and the per-account MCP bearer path (mcp_api_keys).
// Schema authority for the table itself stays upstream in go-panel
// (auth.PgxAccountStore.EnsureSchema); the role-constraint ALTER, the
// accounts-owned mcp_api_keys DDL (mcp_api_keys.sql), and the operator seed
// are owned HERE because they encode go-job policy, not framework shape.
package accounts

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"os"

	"github.com/anatolykoptev/go-panel/auth"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OperatorSeed carries the env-derived operator account to provision.
// Email is the bcrypt login identifier (the framework login form posts an
// <input type="email">); Password is the bcrypt password; Name is display-only.
type OperatorSeed struct {
	Email    string
	Password string
	Name     string
}

// OperatorSeedFromEnv builds the seed from ADMIN_* envs. Email falls back
// ADMIN_EMAIL → ADMIN_USERNAME so a deployment that only ever set the HMAC
// login name still yields a stable account identifier; under the bcrypt driver
// the value should be a real email (the login form enforces it client-side).
func OperatorSeedFromEnv() OperatorSeed {
	email := os.Getenv("ADMIN_EMAIL")
	if email == "" {
		email = os.Getenv("ADMIN_USERNAME")
	}
	return OperatorSeed{
		Email:    email,
		Password: os.Getenv("ADMIN_PASSWORD"),
		Name:     os.Getenv("ADMIN_NAME"),
	}
}

// roleMigrationSQL is the idempotent go-job-owned migration locking
// panel_accounts.role to {'user','admin'} (plan ADR-2/ADR-6). The vendored
// EnsureSchema DDL ships `role TEXT NOT NULL DEFAULT 'admin'`, so:
//
//   - any pre-constraint row carrying a role outside the allowed set (e.g. a
//     hand-inserted 'owner') is normalized to 'admin' FIRST — ADD CONSTRAINT
//     would fail on the violation instead of reporting the real problem;
//   - ALTER COLUMN ... DROP DEFAULT removes the silent 'admin' fill-in — a
//     role-omitting INSERT must fail loud, since every account is provisioned
//     deliberately by the operator;
//   - the CHECK makes 'owner' unwritable: BcryptTOTPAuth.RequireRole's 'owner'
//     super-bypass (bcrypt_auth.go) must stay unreachable — no stored row may
//     ever carry it.
//
// The DO block is the idempotency guard (Postgres has no ADD CONSTRAINT IF NOT
// EXISTS). Safe to run on every boot.
const roleMigrationSQL = `
UPDATE panel_accounts SET role = 'admin' WHERE role NOT IN ('user', 'admin');

ALTER TABLE panel_accounts ALTER COLUMN role DROP DEFAULT;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'panel_accounts_role_check'
          AND conrelid = 'panel_accounts'::regclass
          AND contype = 'c' -- only a CHECK satisfies the guard: a same-named FK/UNIQUE must not suppress the ADD CONSTRAINT
    ) THEN
        ALTER TABLE panel_accounts
            ADD CONSTRAINT panel_accounts_role_check CHECK (role IN ('user', 'admin'));
    END IF;
END $$;`

// mcpAPIKeysSchema is the accounts-owned DDL for mcp_api_keys (plan ADR-3) —
// the per-account MCP bearer substrate KeyStore serves. It lives in this
// package (NOT under any */schema/ dir) and executes inside Bootstrap because
// it REFERENCES panel_accounts: an account-FK table migrates with the schema
// it references, never via a migration runner that could be applied without
// EnsureSchema — CI's standalone psql loop sweeps the schema dirs, which is
// exactly why this file must stay unreachable by any migration glob. Moved
// out of hunt's standard roots (formerly 014_mcp_api_keys.sql): ADR-6 made
// self-contained — every future account-FK table lands here, same rule.
//
//go:embed mcp_api_keys.sql
var mcpAPIKeysSchema string

// Bootstrap runs the boot-time panel_accounts sequence IN ORDER (the ordering
// is load-bearing, ADR-6): EnsureSchema creates the table + TOTP columns
// before any account-owned DDL may FK-reference them; the role-constraint
// migration follows; the mcp_api_keys schema applies next; the env-seeded
// operator admin runs last.
//
// A nil pool is the no-DB deployment shape (DATABASE_URL unset): Bootstrap is
// skipped and returns (nil, nil, nil) — never a nil-deref inside EnsureSchema.
// The bcrypt driver treats nil acctStore as "admin disabled" (fail-closed);
// the hmac rollback does not need a store at all.
//
// Returns the ready store and the seeded operator account — nil account when
// the seed envs are absent (a deployment that never enables the admin UI), or
// when the stored operator row is deactivated (the env seed must not resurrect
// an operator that was deliberately deactivated).
func Bootstrap(ctx context.Context, pool *pgxpool.Pool, seed OperatorSeed) (*auth.PgxAccountStore, *auth.Account, error) {
	if pool == nil {
		return nil, nil, nil
	}
	store := auth.NewPgxAccountStore(pool)
	if err := store.EnsureSchema(ctx); err != nil {
		return nil, nil, fmt.Errorf("accounts: ensure schema: %w", err)
	}
	if _, err := pool.Exec(ctx, roleMigrationSQL); err != nil {
		return nil, nil, fmt.Errorf("accounts: role constraint migration: %w", err)
	}
	if _, err := pool.Exec(ctx, mcpAPIKeysSchema); err != nil {
		return nil, nil, fmt.Errorf("accounts: mcp_api_keys schema: %w", err)
	}
	op, err := seedOperator(ctx, pool, store, seed)
	if err != nil {
		return nil, nil, fmt.Errorf("accounts: seed operator: %w", err)
	}
	return store, op, nil
}

// seedOperator provisions the operator admin account from env (baseline plan
// ADR-J). CreateAccount is ON CONFLICT DO NOTHING, so a restart never clobbers
// the row; UpdatePasswordHash then re-syncs the hash from env on EVERY boot, so
// rotating ADMIN_PASSWORD is a deploy-time env change, not a SQL update.
//
// Role is always 'admin', NEVER 'owner' — the role CHECK added above makes
// 'owner' unwritable and RequireRole's super-bypass unreachable.
//
// The operator account UUID doubles as the static identity the AUTH_DRIVER=hmac
// single-operator rollback pins (see adminui); a deactivated operator row is
// reported but returned (the pin needs the stable ID either way).
func seedOperator(ctx context.Context, pool *pgxpool.Pool, store *auth.PgxAccountStore, seed OperatorSeed) (*auth.Account, error) {
	if seed.Email == "" || seed.Password == "" {
		return nil, nil
	}
	hash, err := auth.HashPassword(seed.Password)
	if err != nil {
		return nil, err
	}
	id, created, err := store.CreateAccount(ctx, seed.Email, seed.Name, hash, "admin")
	if err != nil {
		return nil, err
	}
	if !created {
		// Existing row: resolve its id (CreateAccount returns "" on conflict).
		// Read id+active directly — GetByEmail filters inactive rows, but an
		// existing deactivated operator still gets its password re-synced (env
		// is the source of truth for the credential) while staying deactivated
		// (env must not resurrect it).
		var active bool
		if err := pool.QueryRow(ctx,
			"SELECT id, active FROM panel_accounts WHERE email = $1", seed.Email,
		).Scan(&id, &active); err != nil {
			return nil, fmt.Errorf("resolve seeded operator id: %w", err)
		}
		if !active {
			slog.Warn("accounts: seeded operator account exists but is deactivated — env password synced, account left inactive",
				slog.String("email", seed.Email))
		}
	}
	if err := store.UpdatePasswordHash(ctx, id, hash); err != nil {
		return nil, err
	}
	acct, err := store.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if created {
		slog.Info("accounts: seeded operator admin", slog.String("email", seed.Email))
	}
	return acct, nil
}

// Sentinel tenant slug pinned when AUTH_DRIVER=hmac runs without a resolvable
// operator account (no DATABASE_URL, or the seed envs are absent). Every
// authenticated admin request resolves to this one slot: full single-operator
// mode (ADR-17) — the HMAC session carries no account identity, so the tenant
// must come from configuration, not from the request.
const SingleOperatorSlug = "operator"
