package adminui

import (
	"context"
	"testing"

	"github.com/anatolykoptev/go_job/internal/accounts"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// fixedAccount resolves aid for every request — the test-side stand-in for
// the session/pin account seam (production: accounts.AccountFrom for bcrypt,
// the pinned operator UUID under hmac).
func fixedAccount(aid uuid.UUID) accountResolver {
	return func(context.Context) (uuid.UUID, bool) { return aid, true }
}

// denyAccount resolves no account — the "no identity" shape that must deny
// score writes and render unscored reads.
func denyAccount() accountResolver {
	return func(context.Context) (uuid.UUID, bool) { return uuid.Nil, false }
}

// newTestAccount bootstraps the accounts-owned schema (Bootstrap is
// idempotent — panel_accounts, mcp_api_keys, account_job_scores) and inserts
// a fresh panel_accounts row, returning its UUID. Score writes and
// score-joining listers need a real account in tests.
func newTestAccount(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	_, _, err := accounts.Bootstrap(ctx, pool, accounts.OperatorSeed{})
	require.NoError(t, err, "accounts.Bootstrap")
	aid, _, err := accounts.CreateAccount(ctx, pool,
		"adminui-test-"+uuid.NewString()[:12]+"@example.com", "adminui test", "test-hash", "user")
	require.NoError(t, err, "accounts.CreateAccount")
	return aid
}
