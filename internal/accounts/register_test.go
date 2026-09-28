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

	var role string
	var active bool
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT role, active FROM panel_accounts WHERE email = 'Pending@T.example'`).Scan(&role, &active))
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

	// inactive + passwordless (key-only) → generic. accounts.CreateAccount
	// (the *string-hash variant) stores a real NULL — auth.PgxAccountStore's
	// own CreateAccount takes string and could only store '' (which IS NOT
	// NULL and would wrongly hint).
	keyID, created, err := accounts.CreateAccount(ctx, pool, "keyonly@t.example", "k", nil, "user")
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, acctStore.SetActive(ctx, keyID.String(), false))
	msg, ok = hint(ctx, "keyonly@t.example")
	assert.False(t, ok)
	assert.Empty(t, msg)

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
