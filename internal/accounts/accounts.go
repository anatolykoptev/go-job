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
	"sync"

	"github.com/anatolykoptev/go-panel/auth"
	"github.com/google/uuid"
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

// accountJobScoresSchema is the accounts-owned DDL for account_job_scores
// (plan ADR-6/ADR-15) — the per-account fit-score surface that replaces the
// global hunt_jobs.fit_* columns for every account view. Same rule as
// mcp_api_keys: lives here (never under */schema/) and executes inside
// Bootstrap because it REFERENCES panel_accounts.
//
//go:embed account_job_scores.sql
var accountJobScoresSchema string

// accountHuntSettingsSchema is the accounts-owned DDL for
// account_hunt_settings (plan ADR-7) — the per-account hunt worker knobs that
// replace the single-row hunt_settings table for account-scoped reads and
// writes. Same rule as mcp_api_keys and account_job_scores: lives here
// (never under */schema/) and executes inside Bootstrap because it
// REFERENCES panel_accounts.
//
//go:embed account_hunt_settings.sql
var accountHuntSettingsSchema string

// Bootstrap runs the boot-time panel_accounts sequence IN ORDER (the ordering
// is load-bearing, ADR-6): EnsureSchema creates the table + TOTP columns
// before any account-owned DDL may FK-reference them; the role-constraint
// migration follows; the accounts-owned schemas (mcp_api_keys,
// account_job_scores, account_hunt_settings) apply next; the env-seeded operator admin runs last —
// after it, the transitional backfills copy hunt_jobs.fit_* into
// account_job_scores (BackfillLegacyJobScores), hunt_settings id=1 into the
// operator's account_hunt_settings row (BackfillLegacyHuntSettings), and
// stamp existing hunt_ratings rows with the operator account
// (BackfillHuntRatingsAccount), and resume_persons/resume_vectors follow via
// BackfillResumeAccountData. All are one-shot and idempotent. LAST comes the
// P5 constrain (ConstrainAccountColumns): the ADR-13 data-gate that SETs NOT
// NULL on every expand-half account_id once zero NULLs remain — and refuses
// the boot, listing per-table counts, when they do.
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
var bootstrapMu sync.Mutex

func Bootstrap(ctx context.Context, pool *pgxpool.Pool, seed OperatorSeed) (*auth.PgxAccountStore, *auth.Account, error) {
	if pool == nil {
		return nil, nil, nil
	}
	// Serialize bootstraps in-process: EnsureSchema's CREATE TYPE IF NOT
	// EXISTS races under concurrent bootstraps (pg_type_typname_nsp_index
	// 23505) — parallel tests hit it; a serial bootstrap is also the only
	// sane prod shape. Cross-process serialization stays the CI runner's
	// job (-p 1), same as before.
	bootstrapMu.Lock()
	defer bootstrapMu.Unlock()

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
	if _, err := pool.Exec(ctx, accountJobScoresSchema); err != nil {
		return nil, nil, fmt.Errorf("accounts: account_job_scores schema: %w", err)
	}
	if _, err := pool.Exec(ctx, accountHuntSettingsSchema); err != nil {
		return nil, nil, fmt.Errorf("accounts: account_hunt_settings schema: %w", err)
	}
	// Unconditional (not op-gated): the account scope on hunt_ratings is
	// schema, not seed data — every bootstrap re-ensures it, which is also
	// what lets test cleanup drop the FK and trust the next Bootstrap to
	// restore it.
	if err := EnsureHuntRatingsAccountScope(ctx, pool); err != nil {
		return nil, nil, fmt.Errorf("accounts: hunt_ratings account scope: %w", err)
	}
	// P4 (plan ADR-6/ADR-8/ADR-9/ADR-10): resume_persons + resume_vectors +
	// oversize_responses carry account_id the same way. Unconditional because
	// ConnectResumeDB runs BEFORE Bootstrap — on a fresh DB panel_accounts
	// does not exist when resume schema 008 / oversize schema 003 execute, so
	// their FK DO-blocks skip; these probes re-issue the FK adds here. The
	// oversize sweep additionally purges ownerless spill rows on every boot —
	// a NULL-account spill is unreachable through every scoped read anyway.
	if err := EnsureResumeAccountScope(ctx, pool); err != nil {
		return nil, nil, fmt.Errorf("accounts: resume account scope: %w", err)
	}
	if err := EnsureOversizeAccountScope(ctx, pool); err != nil {
		return nil, nil, fmt.Errorf("accounts: oversize account scope: %w", err)
	}
	op, err := seedOperator(ctx, pool, store, seed)
	if err != nil {
		return nil, nil, fmt.Errorf("accounts: seed operator: %w", err)
	}
	if op != nil {
		if err := BackfillLegacyJobScores(ctx, pool, op.ID); err != nil {
			return nil, nil, fmt.Errorf("accounts: legacy score backfill: %w", err)
		}
		if err := BackfillHuntRatingsAccount(ctx, pool, op.ID); err != nil {
			return nil, nil, fmt.Errorf("accounts: hunt_ratings backfill: %w", err)
		}
		if err := BackfillLegacyHuntSettings(ctx, pool, op.ID); err != nil {
			return nil, nil, fmt.Errorf("accounts: legacy hunt settings backfill: %w", err)
		}
		if err := BackfillResumeAccountData(ctx, pool, op.ID); err != nil {
			return nil, nil, fmt.Errorf("accounts: resume backfill: %w", err)
		}
	}
	// P5 constrain (plan ADR-13) — the data-gated cutover flip: runs AFTER
	// the operator seed and every backfill, so a surviving NULL account_id
	// is data no backfill owns. The step refuses loudly (per-table NULL
	// counts) and leaves the columns nullable on dirty data; on clean data
	// it applies SET NOT NULL plus the unconditional FK/UNIQUE re-asserts.
	// Unconditional by design — it is the gate, not a schema nicety.
	if err := ConstrainAccountColumns(ctx, pool); err != nil {
		return nil, nil, fmt.Errorf("accounts: account constrain: %w", err)
	}
	return store, op, nil
}

// BackfillLegacyJobScores copies the legacy global hunt_jobs.fit_* score
// columns into account_job_scores for the operator account — the expand
// half of the ADR-13 expand/backfill/constrain migration. TRANSITIONAL: it
// exists only until the hunt_jobs.fit_* columns drop post-soak; scoring
// writes from P2 on land in account_job_scores directly, so this is a
// one-shot cutover copy, idempotent across reboots (ON CONFLICT DO NOTHING —
// an already-rescored row keeps its newer value).
//
// hunt_jobs may not exist when Bootstrap runs on a fresh DB (Bootstrap
// precedes hStore.Migrate — ADR-6 ordering), so the table's presence is
// probed via to_regclass first; absent means there is nothing to backfill.
// Only scored jobs copy — a NULL scored_at has nothing to carry over and the
// job stays in the account's unscored pool for the worker sweep.
func BackfillLegacyJobScores(ctx context.Context, pool *pgxpool.Pool, accountID string) error {
	var reg *string
	if err := pool.QueryRow(ctx, "SELECT to_regclass('public.hunt_jobs')").Scan(&reg); err != nil {
		return fmt.Errorf("probe hunt_jobs: %w", err)
	}
	if reg == nil {
		return nil // fresh DB: hunt schema not migrated yet, nothing to copy
	}
	aid, err := uuid.Parse(accountID)
	if err != nil || aid == uuid.Nil {
		return fmt.Errorf("legacy score backfill: invalid account id %q", accountID)
	}
	ct, err := pool.Exec(ctx, `
		INSERT INTO account_job_scores
			(account_id, job_id, fit_score, fit_band, success_band, over_under, score_rationale, scored_at)
		SELECT $1, j.id, j.fit_score, j.fit_band, j.success_band, j.over_under, j.score_rationale, j.scored_at
		FROM hunt_jobs j
		WHERE j.scored_at IS NOT NULL
		ON CONFLICT (account_id, job_id) DO NOTHING`, aid)
	if err != nil {
		return err
	}
	if n := ct.RowsAffected(); n > 0 {
		slog.Info("accounts: backfilled legacy global scores into account_job_scores",
			slog.String("account_id", accountID), slog.Int64("rows", n))
	}
	return nil
}

// BackfillHuntRatingsAccount stamps existing hunt_ratings rows with the
// operator account and performs the expand-half column add (plan ADR-6/13).
// Bootstrap runs BEFORE hStore.Migrate, so on a live DB the table exists but
// account_id does not yet — the ADD COLUMN IF NOT EXISTS here is the same
// statement hunt schema 014 re-issues (idempotent either way). On a fresh
// DB the table is absent → nothing to backfill; 014 creates the column
// empty.
//
// Multi-user legacy rows collapse deterministically before the UPDATE: rows
// sharing (entry_kind, entry_id) but differing in user_name would violate
// UNIQUE(entry_kind, entry_id, account_id) once stamped with the same
// operator id, so the freshest updated_at row wins and the rest delete.
// Single-operator deployments carry at most one row per (kind,id) — the
// DELETE is a no-op safety net there.
func BackfillHuntRatingsAccount(ctx context.Context, pool *pgxpool.Pool, accountID string) error {
	var reg *string
	if err := pool.QueryRow(ctx, "SELECT to_regclass('public.hunt_ratings')").Scan(&reg); err != nil {
		return fmt.Errorf("probe hunt_ratings: %w", err)
	}
	if reg == nil {
		return nil // fresh DB: hunt schema not migrated yet, nothing to backfill
	}
	aid, err := uuid.Parse(accountID)
	if err != nil || aid == uuid.Nil {
		return fmt.Errorf("hunt_ratings backfill: invalid account id %q", accountID)
	}
	if _, err := pool.Exec(ctx,
		`ALTER TABLE hunt_ratings ADD COLUMN IF NOT EXISTS account_id UUID`); err != nil {
		return fmt.Errorf("hunt_ratings backfill: add account_id: %w", err)
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM hunt_ratings a USING hunt_ratings b
		WHERE a.account_id IS NULL AND b.account_id IS NULL
		  AND a.entry_kind = b.entry_kind AND a.entry_id = b.entry_id
		  AND (a.updated_at < b.updated_at
		       OR (a.updated_at = b.updated_at AND a.id < b.id))`); err != nil {
		return fmt.Errorf("hunt_ratings backfill: dedupe: %w", err)
	}
	ct, err := pool.Exec(ctx,
		`UPDATE hunt_ratings SET account_id = $1 WHERE account_id IS NULL`, aid)
	if err != nil {
		return fmt.Errorf("hunt_ratings backfill: stamp account: %w", err)
	}
	if n := ct.RowsAffected(); n > 0 {
		slog.Info("accounts: backfilled hunt_ratings rows to operator account",
			slog.String("account_id", accountID), slog.Int64("rows", n))
	}
	return nil
}

// EnsureHuntRatingsAccountScope applies the expand-half column add and the
// guarded account_id FK on hunt_ratings — the same statements hunt schema
// 014 re-issues. It runs on EVERY Bootstrap (not only when an operator is
// seeded): the scope is schema, and test cleanup drops the FK alongside
// panel_accounts, so each bootstrap must restore it. No-ops when
// hunt_ratings is absent (fresh DB — Bootstrap precedes hStore.Migrate)
// or when the constraint already exists.
func EnsureHuntRatingsAccountScope(ctx context.Context, pool *pgxpool.Pool) error {
	var reg *string
	if err := pool.QueryRow(ctx, "SELECT to_regclass('public.hunt_ratings')").Scan(&reg); err != nil {
		return fmt.Errorf("probe hunt_ratings: %w", err)
	}
	if reg == nil {
		return nil // fresh DB: hunt schema not migrated yet; 014 creates the scope
	}
	if _, err := pool.Exec(ctx,
		`ALTER TABLE hunt_ratings ADD COLUMN IF NOT EXISTS account_id UUID`); err != nil {
		return fmt.Errorf("hunt_ratings scope: add account_id: %w", err)
	}
	// Orphan sweep before the FK: rows stamped with an account that no longer
	// exists (e.g. test cleanup dropped panel_accounts and re-created it
	// empty) would violate the re-added constraint. An ownerless rating is
	// meaningless — delete, never NULL out. In prod the FK has been enforced
	// since first apply so this is a no-op there; it only fires when the
	// constraint is absent, which is exactly when the sweep is needed.
	if _, err := pool.Exec(ctx, `
		DELETE FROM hunt_ratings
		WHERE account_id IS NOT NULL
		  AND account_id NOT IN (SELECT id FROM panel_accounts)`); err != nil {
		return fmt.Errorf("hunt_ratings scope: orphan sweep: %w", err)
	}
	if _, err := pool.Exec(ctx, `
		DO $$
		BEGIN
			IF NOT EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conname = 'hunt_ratings_account_id_fkey'
				  AND conrelid = 'hunt_ratings'::regclass
			) THEN
				ALTER TABLE hunt_ratings
					ADD CONSTRAINT hunt_ratings_account_id_fkey
					FOREIGN KEY (account_id) REFERENCES panel_accounts(id);
			END IF;
		END $$`); err != nil {
		return fmt.Errorf("hunt_ratings scope: account FK: %w", err)
	}
	return nil
}

// BackfillLegacyHuntSettings copies the legacy single-row hunt_settings
// (id=1) into the operator's account_hunt_settings row preserving EVERY
// stored value — including enabled and score_fail_open (plan ADR-7: the
// operator keeps today's behavior; only NEW accounts default to
// disabled/fail-closed). One-shot, idempotent (ON CONFLICT DO NOTHING — an
// already-edited account row is never clobbered).
//
// Fleet-global knobs (interval_seconds, score_sweep_limit, the fleet LLM
// cap) deliberately do NOT migrate — they are env/deploy config now. The
// legacy hunt_settings row itself stays until the post-soak drop (ADR-13).
// hunt_settings may not exist on a fresh DB (Bootstrap precedes
// hStore.Migrate) → probed via to_regclass like the other backfills.
func BackfillLegacyHuntSettings(ctx context.Context, pool *pgxpool.Pool, accountID string) error {
	var reg *string
	if err := pool.QueryRow(ctx, "SELECT to_regclass('public.hunt_settings')").Scan(&reg); err != nil {
		return fmt.Errorf("probe hunt_settings: %w", err)
	}
	if reg == nil {
		return nil // fresh DB: nothing to migrate
	}
	aid, err := uuid.Parse(accountID)
	if err != nil || aid == uuid.Nil {
		return fmt.Errorf("legacy hunt settings backfill: invalid account id %q", accountID)
	}
	ct, err := pool.Exec(ctx, `
		INSERT INTO account_hunt_settings
			(account_id, enabled, queries, notify_chat_id, notify_min_fit,
			 notify_max_age_seconds, score_enabled, score_min_jaccard,
			 score_max_llm_per_cycle, score_fail_open, updated_at)
		SELECT $1, enabled, queries, notify_chat_id, notify_min_fit,
		       notify_max_age_seconds, score_enabled, score_min_jaccard,
		       score_max_llm_per_cycle, score_fail_open, updated_at
		FROM hunt_settings WHERE id = 1
		ON CONFLICT (account_id) DO NOTHING`, aid)
	if err != nil {
		return fmt.Errorf("legacy hunt settings backfill: %w", err)
	}
	if n := ct.RowsAffected(); n > 0 {
		slog.Info("accounts: migrated hunt_settings row into operator account_hunt_settings",
			slog.String("account_id", accountID))
	}
	return nil
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

// ─── P4 scope: resume cluster + oversize (plan ADR-6/8/9/10) ─────────────────

// EnsureResumeAccountScope applies the expand-half column adds and the guarded
// account_id FKs on resume_persons and resume_vectors, re-issues the vector
// UNIQUE swap ((user_name, content_hash) → (account_id, content_hash)) and the
// account indexes — the same statements resume schema 008 runs. It runs on
// EVERY Bootstrap because ConnectResumeDB precedes it: on a fresh DB
// panel_accounts does not exist when 008 executes, so its guarded DO blocks
// skip the FKs; this probe re-issues them once the identity root exists.
// No-ops when the resume tables are absent (fresh DB — Bootstrap precedes
// hStore.Migrate AND ConnectResumeDB may not have run in tests).
func EnsureResumeAccountScope(ctx context.Context, pool *pgxpool.Pool) error {
	var reg *string
	if err := pool.QueryRow(ctx, "SELECT to_regclass('public.resume_persons')").Scan(&reg); err != nil {
		return fmt.Errorf("probe resume_persons: %w", err)
	}
	if reg == nil {
		return nil // fresh DB: resume schema not migrated yet; 008 creates the scope
	}
	if _, err := pool.Exec(ctx, `
		ALTER TABLE resume_persons ADD COLUMN IF NOT EXISTS account_id UUID;
		ALTER TABLE resume_vectors ADD COLUMN IF NOT EXISTS account_id UUID;`); err != nil {
		return fmt.Errorf("resume scope: add account_id columns: %w", err)
	}
	// Orphan sweep before the FKs — same rationale as the hunt_ratings sweep in
	// EnsureHuntRatingsAccountScope: ownerless rows would violate a re-added
	// constraint (test cleanup drops panel_accounts and recreates it empty).
	if _, err := pool.Exec(ctx, `
		DELETE FROM resume_persons
		WHERE account_id IS NOT NULL
		  AND account_id NOT IN (SELECT id FROM panel_accounts);
		DELETE FROM resume_vectors
		WHERE account_id IS NOT NULL
		  AND account_id NOT IN (SELECT id FROM panel_accounts)`); err != nil {
		return fmt.Errorf("resume scope: orphan sweep: %w", err)
	}
	if _, err := pool.Exec(ctx, `
		DO $$
		BEGIN
			IF NOT EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conname = 'resume_persons_account_id_fkey'
				  AND conrelid = 'resume_persons'::regclass
			) THEN
				ALTER TABLE resume_persons
					ADD CONSTRAINT resume_persons_account_id_fkey
					FOREIGN KEY (account_id) REFERENCES panel_accounts(id);
			END IF;
			IF NOT EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conname = 'resume_vectors_account_id_fkey'
				  AND conrelid = 'resume_vectors'::regclass
			) THEN
				ALTER TABLE resume_vectors
					ADD CONSTRAINT resume_vectors_account_id_fkey
					FOREIGN KEY (account_id) REFERENCES panel_accounts(id);
			END IF;
		END $$`); err != nil {
		return fmt.Errorf("resume scope: account FKs: %w", err)
	}
	// The vector dedup scope swap tolerates pre-backfill NULLs (NULLs are
	// distinct under UNIQUE); NOT NULL enforcement is deferred to P5 (ADR-13).
	if _, err := pool.Exec(ctx, `
		ALTER TABLE resume_vectors DROP CONSTRAINT IF EXISTS resume_vectors_user_name_content_hash_key;
		DO $$
		BEGIN
			IF NOT EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conname = 'resume_vectors_account_content_key'
				  AND conrelid = 'resume_vectors'::regclass
			) THEN
				ALTER TABLE resume_vectors
					ADD CONSTRAINT resume_vectors_account_content_key
					UNIQUE (account_id, content_hash);
			END IF;
		END $$;
		CREATE INDEX IF NOT EXISTS idx_resume_persons_account ON resume_persons (account_id);
		CREATE INDEX IF NOT EXISTS idx_resume_vectors_account ON resume_vectors (account_id);`); err != nil {
		return fmt.Errorf("resume scope: vector unique swap/indexes: %w", err)
	}
	return nil
}

// EnsureOversizeAccountScope applies the expand-half column add and the
// guarded account_id FK on oversize_responses, then purges ownerless spill
// rows — plan ADR-10/backfill note: spill rows are a TTL'd cache, so unowned
// rows are DELETED rather than guessed-stamped to an account. The purge runs
// unconditionally on every Bootstrap: a NULL-account row is unreachable
// through every scoped read, so dropping it early is always safe.
// No-ops when oversize_responses is absent (Bootstrap precedes
// oversize.Migrate on a fresh DB — schema 003 creates the scope there).
func EnsureOversizeAccountScope(ctx context.Context, pool *pgxpool.Pool) error {
	var reg *string
	if err := pool.QueryRow(ctx, "SELECT to_regclass('public.oversize_responses')").Scan(&reg); err != nil {
		return fmt.Errorf("probe oversize_responses: %w", err)
	}
	if reg == nil {
		return nil // fresh DB: oversize schema not migrated yet; 003 creates the scope
	}
	if _, err := pool.Exec(ctx,
		`ALTER TABLE oversize_responses ADD COLUMN IF NOT EXISTS account_id UUID`); err != nil {
		return fmt.Errorf("oversize scope: add account_id: %w", err)
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM oversize_responses
		WHERE account_id IS NULL
		   OR account_id NOT IN (SELECT id FROM panel_accounts)`); err != nil {
		return fmt.Errorf("oversize scope: unowned-row purge: %w", err)
	}
	if _, err := pool.Exec(ctx, `
		DO $$
		BEGIN
			IF NOT EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conname = 'oversize_responses_account_id_fkey'
				  AND conrelid = 'oversize_responses'::regclass
			) THEN
				ALTER TABLE oversize_responses
					ADD CONSTRAINT oversize_responses_account_id_fkey
					FOREIGN KEY (account_id) REFERENCES panel_accounts(id);
			END IF;
		END $$;
		CREATE INDEX IF NOT EXISTS idx_oversize_responses_account
			ON oversize_responses (account_id, created_at DESC)`); err != nil {
		return fmt.Errorf("oversize scope: account FK/index: %w", err)
	}
	return nil
}

// BackfillResumeAccountData stamps existing resume_persons and resume_vectors
// rows with the operator account and re-keys every stamped vector's
// content_hash to sha256(<op-uuid>|<mem_type>|<coalesce(ref_id,0)>|<content>)
// — the same digest the account-scoped writers compute in
// jobs.vectorContentHash (plan ADR-9). Legacy rows carried
// sha256(user_name|...); after stamping, dedup must still recognise them, so
// the hash is recomputed over the NEW account key. pgcrypto's sha256() is a
// core function (PG 11+), no extension needed.
//
// Dedupe precedes the stamp: two legacy rows differing only in user_name
// collapse to the same (account_id, content_hash) pair once re-keyed — the
// freshest updated_at row wins, matching the hunt_ratings collapse rule. Rows
// colliding with an ALREADY-stamped operator row are dropped first so a
// repeated/interleaved backfill can never violate the new UNIQUE.
//
// resume_persons rows simply stamp — the persons table has no uniqueness
// constraint to collide with, and every child row follows the parent's
// ownership transitively via person_id.
func BackfillResumeAccountData(ctx context.Context, pool *pgxpool.Pool, accountID string) error {
	var reg *string
	if err := pool.QueryRow(ctx, "SELECT to_regclass('public.resume_persons')").Scan(&reg); err != nil {
		return fmt.Errorf("probe resume_persons: %w", err)
	}
	if reg == nil {
		return nil // fresh DB: resume schema not migrated yet, nothing to backfill
	}
	aid, err := uuid.Parse(accountID)
	if err != nil || aid == uuid.Nil {
		return fmt.Errorf("resume backfill: invalid account id %q", accountID)
	}

	ct, err := pool.Exec(ctx,
		`UPDATE resume_persons SET account_id = $1 WHERE account_id IS NULL`, aid)
	if err != nil {
		return fmt.Errorf("resume backfill: stamp persons: %w", err)
	}
	if n := ct.RowsAffected(); n > 0 {
		slog.Info("accounts: backfilled resume_persons rows to operator account",
			slog.String("account_id", accountID), slog.Int64("rows", n))
	}

	// Drop NULL-account rows whose (mem_type, ref_id, content) is already
	// held by the operator (or a fresher NULL twin): after re-keying both
	// would hash identically under the operator account.
	if _, err := pool.Exec(ctx, `
		DELETE FROM resume_vectors a USING resume_vectors b
		WHERE a.account_id IS NULL
		  AND (b.account_id IS NULL OR b.account_id = $1)
		  AND a.id <> b.id
		  AND a.mem_type = b.mem_type
		  AND COALESCE(a.ref_id, 0) = COALESCE(b.ref_id, 0)
		  AND a.content = b.content
		  AND (a.updated_at < b.updated_at
		       OR (a.updated_at = b.updated_at AND a.id < b.id))`, aid); err != nil {
		return fmt.Errorf("resume backfill: vector dedupe: %w", err)
	}
	// $1 binds the uuid for the column write; $2 binds the canonical text
	// form for the hash — one param can't be deduced as both uuid and text
	// (SQLSTATE 42P08). The text form must match jobs.vectorContentHash.
	ct, err = pool.Exec(ctx, `
		UPDATE resume_vectors
		SET account_id = $1,
		    content_hash = encode(sha256(
		        ($2 || '|' || mem_type || '|' || COALESCE(ref_id, 0)::text || '|' || content)::bytea
		    ), 'hex')
		WHERE account_id IS NULL`, aid, aid.String())
	if err != nil {
		return fmt.Errorf("resume backfill: stamp/re-key vectors: %w", err)
	}
	if n := ct.RowsAffected(); n > 0 {
		slog.Info("accounts: backfilled resume_vectors rows to operator account (content_hash re-keyed)",
			slog.String("account_id", accountID), slog.Int64("rows", n))
	}
	return nil
}
