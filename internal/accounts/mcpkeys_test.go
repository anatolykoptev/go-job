package accounts_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anatolykoptev/go-panel/auth"
	"github.com/anatolykoptev/go_job/internal/accounts"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/stretchr/testify/require"
)

// openKeyStore builds the full P1 substrate on a real ephemeral database in
// the same order initEngine does — accounts.Bootstrap (panel_accounts)
// BEFORE the standard hunt migration runner whose 014 file owns mcp_api_keys
// and REFERENCES panel_accounts (ADR-6 ordering, mirrored deliberately).
func openKeyStore(t *testing.T, seed accounts.OperatorSeed) (*accounts.KeyStore, *auth.PgxAccountStore, *auth.Account, *pgxpool.Pool) {
	t.Helper()
	pool := openTestPool(t)
	ctx := context.Background()
	dropAccountTables(t, pool)
	acctStore, op, err := accounts.Bootstrap(ctx, pool, seed)
	require.NoError(t, err)
	require.NoError(t, hunt.NewStore(pool).Migrate(ctx),
		"standard migration roots must apply cleanly — migration 014 owns mcp_api_keys")
	return accounts.NewKeyStore(pool), acctStore, op, pool
}

func verifyToken(t *testing.T, ks *accounts.KeyStore, token string) (*sdkauth.TokenInfo, error) {
	t.Helper()
	return ks.Verifier()(context.Background(), token, httptest.NewRequest(http.MethodGet, "/mcp", nil))
}

func opUUID(t *testing.T, op *auth.Account) uuid.UUID {
	t.Helper()
	require.NotNil(t, op, "operator seed must produce an account")
	id, err := uuid.Parse(op.ID)
	require.NoError(t, err)
	return id
}

// TestKeyStore_MintThenVerify is the happy path: a key minted through the real
// store authenticates and resolves to the owning account's UUID — the
// UserID every P2+ consumer reads via accounts.AccountFrom.
func TestKeyStore_MintThenVerify(t *testing.T) {
	ks, _, op, _ := openKeyStore(t, accounts.OperatorSeed{Email: "mint@t.example", Password: "pw-123456"})
	ctx := context.Background()

	token, err := ks.Mint(ctx, opUUID(t, op), "smoke")
	require.NoError(t, err)
	require.NotEmpty(t, token)

	ti, err := verifyToken(t, ks, token)
	require.NoError(t, err)
	require.Equal(t, op.ID, ti.UserID, "TokenInfo.UserID must be the account UUID — AccountFrom parses it")
	require.False(t, ti.Expiration.IsZero(),
		"SDK verify() rejects zero Expiration — the verifier must always stamp one")
}

// TestKeyStore_DenyMatrix covers every closed leg of the verifier:
// wrong/empty token, revoked key (immediate, stateless), deactivated account.
func TestKeyStore_DenyMatrix(t *testing.T) {
	ks, acctStore, op, pool := openKeyStore(t, accounts.OperatorSeed{Email: "deny@t.example", Password: "pw-123456"})
	ctx := context.Background()
	opID := opUUID(t, op)

	token, err := ks.Mint(ctx, opID, "victim")
	require.NoError(t, err)

	if _, err = verifyToken(t, ks, "gj_wrong_token_entirely"); !errors.Is(err, sdkauth.ErrInvalidToken) {
		t.Fatalf("unknown token must map to ErrInvalidToken, got %v", err)
	}
	if _, err = verifyToken(t, ks, ""); !errors.Is(err, sdkauth.ErrInvalidToken) {
		t.Fatalf("empty token must map to ErrInvalidToken, got %v", err)
	}

	// Revoke → denied on the very next request (no cache, no propagation lag).
	var keyID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM mcp_api_keys WHERE key_prefix = $1`, accounts.KeyPrefix(token)).Scan(&keyID))
	require.NoError(t, ks.Revoke(ctx, keyID))
	if _, err = verifyToken(t, ks, token); !errors.Is(err, sdkauth.ErrInvalidToken) {
		t.Fatalf("revoked key must be denied immediately, got %v", err)
	}

	// Deactivated account → denied; reactivate → accepted again (the JOIN on
	// a.active is doing the work).
	token2, err := ks.Mint(ctx, opID, "second")
	require.NoError(t, err)
	require.NoError(t, acctStore.SetActive(ctx, op.ID, false))
	if _, err = verifyToken(t, ks, token2); !errors.Is(err, sdkauth.ErrInvalidToken) {
		t.Fatalf("deactivated account's key must be denied, got %v", err)
	}
	require.NoError(t, acctStore.SetActive(ctx, op.ID, true))
	if _, err = verifyToken(t, ks, token2); err != nil {
		t.Fatalf("reactivated account's key must verify again, got %v", err)
	}

	// Double-revoke is a loud no-op for the CLI — the row must not resurrect.
	require.Error(t, ks.Revoke(ctx, keyID))
	if _, err = verifyToken(t, ks, token); !errors.Is(err, sdkauth.ErrInvalidToken) {
		t.Fatalf("double-revoked key must stay denied, got %v", err)
	}
}

// TestKeyStore_SeedEdgeToken covers the ADR-4 zero-window seed: idempotent
// across restarts, raw token never persisted, the seeded token authenticates
// through the same verifier path as minted keys.
func TestKeyStore_SeedEdgeToken(t *testing.T) {
	ks, _, op, pool := openKeyStore(t, accounts.OperatorSeed{Email: "seed@t.example", Password: "pw-123456"})
	ctx := context.Background()
	opID := opUUID(t, op)
	const edgeToken = "edge-token-from-caddy-map-0123456789"

	inserted, err := ks.SeedEdgeToken(ctx, opID, edgeToken, "legacy-edge-token")
	require.NoError(t, err)
	require.True(t, inserted)

	// Restart idempotency: second seed is a no-op, row count stays 1.
	again, err := ks.SeedEdgeToken(ctx, opID, edgeToken, "legacy-edge-token")
	require.NoError(t, err)
	require.False(t, again, "ON CONFLICT DO NOTHING — restarts must not duplicate or clobber")
	var n int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM mcp_api_keys WHERE account_id = $1`, opID).Scan(&n))
	require.Equal(t, 1, n)

	// The raw token never lands in the table — prefix is the first-8 fragment.
	var prefix string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT key_prefix FROM mcp_api_keys WHERE label = 'legacy-edge-token'`).Scan(&prefix))
	require.Equal(t, edgeToken[:8], prefix)
	require.NotEqual(t, edgeToken, prefix)

	// The seeded token authenticates and resolves to the operator account.
	ti, err := verifyToken(t, ks, edgeToken)
	require.NoError(t, err)
	require.Equal(t, op.ID, ti.UserID)
}
