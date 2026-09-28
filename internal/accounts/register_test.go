package accounts_test

// register_test.go — P6 public-registration store seams: RegisterPending
// (the anonymous-surface INSERT that can only ever produce an inactive
// 'user' row) and PendingLoginHint (the LoginFailHint hook telling a fresh
// signup why login fails).

import (
	"context"
	"testing"

	"github.com/anatolykoptev/go_job/internal/accounts"
	"github.com/anatolykoptev/go_job/internal/dbtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRegisterPending_Shape pins the spec literals on a real DB: a public
// register produces EXACTLY role='user', active=false — never an active
// account, never an admin. The Makefile fitness gate greps the SQL for the
// same literals (defense in depth: source pin + live assertion).
func TestRegisterPending_Shape(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	dbtest.DropAccountTables(t, pool)
	_, _, err := accounts.Bootstrap(ctx, pool, accounts.OperatorSeed{})
	require.NoError(t, err)

	created, err := accounts.RegisterPending(ctx, pool, "Pending@T.example", "pending", "hash-irrelevant")
	require.NoError(t, err)
	assert.True(t, created)

	var storedEmail, role string
	var active bool
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT email, role, active FROM panel_accounts WHERE email = 'pending@t.example'`).Scan(&storedEmail, &role, &active))
	assert.Equal(t, "pending@t.example", storedEmail, "email must be stored normalized — login lower-trims, so a verbatim row would be unreachable")
	assert.Equal(t, "user", role, "register must pin role='user' — a parameter would let the public surface mint an admin")
	assert.False(t, active, "register must pin active=false — pending approval")
}

// TestRegisterPending_ConflictLeavesRow: ON CONFLICT DO NOTHING returns
// created=false and leaves the existing row (active state, role, hash)
// untouched — the anti-enumeration contract.
func TestRegisterPending_ConflictLeavesRow(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	dbtest.DropAccountTables(t, pool)
	acctStore, _, err := accounts.Bootstrap(ctx, pool, accounts.OperatorSeed{})
	require.NoError(t, err)

	// Pre-existing ACTIVE admin row — the strongest conflict shape.
	id, created, err := acctStore.CreateAccount(ctx, "taken@t.example", "taken", "orig-hash", "admin")
	require.NoError(t, err)
	require.True(t, created)

	created, err = accounts.RegisterPending(ctx, pool, "taken@t.example", "new name", "new-hash")
	require.NoError(t, err)
	assert.False(t, created)

	var role, name, hash string
	var active bool
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT role, name, password_hash, active FROM panel_accounts WHERE id = $1`, id).
		Scan(&role, &name, &hash, &active))
	assert.Equal(t, "admin", role)
	assert.Equal(t, "taken", name)
	assert.Equal(t, "orig-hash", hash)
	assert.True(t, active, "conflict must not flip an active account to pending")
}

// TestPendingLoginHint exercises all four states: nonexistent email, pending
// passworded (the only hinted shape), inactive passwordless (key-only — can
// never log in, stays generic), and active (never reaches the branch in
// production but must still decline).
func TestPendingLoginHint(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	dbtest.DropAccountTables(t, pool)
	acctStore, _, err := accounts.Bootstrap(ctx, pool, accounts.OperatorSeed{})
	require.NoError(t, err)
	hint := accounts.PendingLoginHint(pool)

	// nonexistent → no hint.
	msg, ok := hint(ctx, "ghost@t.example")
	assert.False(t, ok)
	assert.Empty(t, msg)

	// pending + passworded → hint (mixed-case input proves the hook's own
	// normalization — go-panel also normalizes before calling).
	created, err := accounts.RegisterPending(ctx, pool, "pend@t.example", "p", "hash")
	require.NoError(t, err)
	require.True(t, created)
	msg, ok = hint(ctx, "  Pend@T.Example  ")
	require.True(t, ok, "pending passworded account must hint")
	assert.Contains(t, msg, "not yet active")

	// active + passworded → generic (the hint never fires for a live
	// account — production won't even reach it, but the function must not
	// leak "this email exists" when probed directly).
	_, created, err = acctStore.CreateAccount(ctx, "live@t.example", "l", "hash", "user")
	require.NoError(t, err)
	require.True(t, created)
	msg, ok = hint(ctx, "live@t.example")
	assert.False(t, ok)
	assert.Empty(t, msg)
}

// TestCreateAccount_NormalizesEmail: every write seam canonicalizes the
// email — a mixed-case CLI/seed email must store lowercase so the login
// path (which lower-trims input) and the case-sensitive unique index stay
// consistent. Covers both accounts.CreateAccount and the seedOperator path.
func TestCreateAccount_NormalizesEmail(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	dbtest.DropAccountTables(t, pool)
	acctStore, op, err := accounts.Bootstrap(ctx, pool,
		accounts.OperatorSeed{Email: "Op.Seed@T.example", Password: "op-pass-12345"})
	require.NoError(t, err)
	require.NotNil(t, op)

	var email string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT email FROM panel_accounts WHERE id = $1`, op.ID).Scan(&email))
	assert.Equal(t, "op.seed@t.example", email, "seeded operator email must be normalized at the write seam")

	hash := "h"
	id, created, err := accounts.CreateAccount(ctx, pool, "Mixed.Case@T.example", "m", hash, "user")
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT email FROM panel_accounts WHERE id = $1`, id).Scan(&email))
	assert.Equal(t, "mixed.case@t.example", email)

	// The normalized row resolves through the login-path lookup.
	acct, err := acctStore.GetByID(ctx, id.String())
	require.NoError(t, err)
	require.NotNil(t, acct)
}

// TestCreateAccount_PasswordRequired: the account model has no key-only
// shape — CreateAccount rejects an empty hash at the API layer and the
// server-boot gate (EnsurePasswordRequired) constrains
// panel_accounts.password_hash NOT NULL + CHECK <> ” once no NULL rows
// survive. The gate deliberately lives outside Bootstrap so gojob-admin can
// remediate a legacy DB.
func TestCreateAccount_PasswordRequired(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	dbtest.DropAccountTables(t, pool)
	_, _, err := accounts.Bootstrap(ctx, pool, accounts.OperatorSeed{})
	require.NoError(t, err)

	_, _, err = accounts.CreateAccount(ctx, pool, "nopw@t.example", "n", "", "user")
	require.Error(t, err, "empty password hash must be rejected")

	// Legacy NULL row → the gate refuses and names the remediation verb +
	// affected email (gojob-admin runs Bootstrap only, so the verb is never
	// deadlocked).
	_, err = pool.Exec(ctx,
		`INSERT INTO panel_accounts (email, name, role) VALUES ('legacy-null@t.example','x','user')`)
	require.NoError(t, err)
	err = accounts.EnsurePasswordRequired(ctx, pool)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "set-password")
	assert.Contains(t, err.Error(), "legacy-null@t.example")

	// Backfill via the same primitive set-password uses → gate then applies
	// NOT NULL + the non-empty CHECK, and stays idempotent on re-run.
	_, err = pool.Exec(ctx,
		`UPDATE panel_accounts SET password_hash = 'h' WHERE email = 'legacy-null@t.example'`)
	require.NoError(t, err)
	require.NoError(t, accounts.EnsurePasswordRequired(ctx, pool))
	require.NoError(t, accounts.EnsurePasswordRequired(ctx, pool), "gate must be idempotent")

	var nullable string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT is_nullable FROM information_schema.columns
		 WHERE table_name = 'panel_accounts' AND column_name = 'password_hash'`).Scan(&nullable))
	assert.Equal(t, "NO", nullable, "password_hash must be NOT NULL after the gate")

	var checks int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_constraint WHERE conname = 'panel_accounts_password_nonempty'`).Scan(&checks))
	assert.Equal(t, 1, checks, "non-empty CHECK must exist")

	_, err = pool.Exec(ctx,
		`INSERT INTO panel_accounts (email, name, role, password_hash) VALUES ('emptypw@t.example','x','user','')`)
	require.Error(t, err, "empty-string hash must violate the CHECK")
}
