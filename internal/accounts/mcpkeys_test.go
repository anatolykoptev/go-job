package accounts_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/anatolykoptev/go-panel/auth"
	"github.com/anatolykoptev/go_job/internal/accounts"
	"github.com/anatolykoptev/go_job/internal/dbtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/stretchr/testify/require"
)

// openKeyStore builds the full P1 substrate on a real ephemeral database the
// same way initEngine does — accounts.Bootstrap provisions panel_accounts AND
// the accounts-owned mcp_api_keys DDL in one ordered pass (ADR-6
// self-contained: the table migrates with the schema it references, not via
// the standard migration roots).
func openKeyStore(t *testing.T, seed accounts.OperatorSeed) (*accounts.KeyStore, *auth.PgxAccountStore, *auth.Account, *pgxpool.Pool) {
	t.Helper()
	pool := openTestPool(t)
	ctx := context.Background()
	dbtest.DropAccountTables(t, pool)
	acctStore, op, err := accounts.Bootstrap(ctx, pool, seed)
	require.NoError(t, err)
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

	// Boundary: at/under keyPrefixLen the seed refuses — key_prefix would
	// persist the whole token. One char above the floor lands fine.
	_, err = ks.SeedEdgeToken(ctx, opID, "tiny", "x")
	require.Error(t, err, "len <= keyPrefixLen must be rejected")
	_, err = ks.SeedEdgeToken(ctx, opID, "123456789", "boundary-9")
	require.NoError(t, err, "9 chars passes the floor")

	// The seeded token authenticates and resolves to the operator account.
	ti, err := verifyToken(t, ks, edgeToken)
	require.NoError(t, err)
	require.Equal(t, op.ID, ti.UserID)
}

// TestDenyAllVerifier_FailClosed is the HIGH-1 DB-down leg: when DATABASE_URL
// is configured but ConnectResumeDB failed at boot, :8891 mounts
// DenyAllVerifier instead of serving /mcp unauthenticated. Proven through the
// REAL go-sdk RequireBearerToken middleware — a bearer token gets 401, and no
// token gets 401 too (the middleware short-circuits before the verifier).
func TestDenyAllVerifier_FailClosed(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := sdkauth.RequireBearerToken(accounts.DenyAllVerifier(), nil)(next)

	for _, tok := range []string{"gj_legitlookingtokenvalue", "x"} {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		require.Equal(t, http.StatusUnauthorized, rr.Code,
			"deny-all verifier must 401 every token while the accounts DB is down")
	}
}

// TestKeyStore_SeedEdgeToken_ShortRejected pins the key_prefix floor: a token
// at or under keyPrefixLen would persist the WHOLE credential in the
// key_prefix column — the seed refuses before touching the DB, so a nil pool
// exercises the gate without a database.
func TestKeyStore_SeedEdgeToken_ShortRejected(t *testing.T) {
	ks := accounts.NewKeyStore(nil)
	ctx := context.Background()
	for _, tok := range []string{"", "short", "12345678"} {
		_, err := ks.SeedEdgeToken(ctx, uuid.New(), tok, "x")
		require.Error(t, err, "token len %d must be rejected — key_prefix would hold the whole token", len(tok))
	}
}

// TestKeyStore_RevokeForAccount pins the self-serve revoke scoping (P6):
// account-scoped revocation touches only the caller's own keys — a foreign
// key id collapses to the same "not found" as a nonexistent one (no
// ownership oracle), and the victim's key stays live through the verifier.
func TestKeyStore_RevokeForAccount(t *testing.T) {
	ks, _, op, pool := openKeyStore(t, accounts.OperatorSeed{Email: "rev-self@t.example", Password: "pw-123456"})
	ctx := context.Background()
	owner := opUUID(t, op)

	other, created, err := accounts.CreateAccount(ctx, pool, "rev-other@t.example", "other", "test-hash", "user")
	require.NoError(t, err)
	require.True(t, created)

	ownerTok, err := ks.Mint(ctx, owner, "mine")
	require.NoError(t, err)
	otherTok, err := ks.Mint(ctx, other, "theirs")
	require.NoError(t, err)
	var ownerKeyID, otherKeyID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM mcp_api_keys WHERE key_prefix = $1`, accounts.KeyPrefix(ownerTok)).Scan(&ownerKeyID))
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM mcp_api_keys WHERE key_prefix = $1`, accounts.KeyPrefix(otherTok)).Scan(&otherKeyID))

	// Foreign id → identical error to nonexistent, and the victim row is
	// untouched: it still verifies through the real bearer path.
	err = ks.RevokeForAccount(ctx, owner, otherKeyID)
	require.Error(t, err, "foreign revoke must fail")
	require.Contains(t, err.Error(), "not found or already revoked")
	if _, verr := verifyToken(t, ks, otherTok); verr != nil {
		t.Fatalf("foreign revoke must not touch the victim's key, got %v", verr)
	}
	var revokedAt *time.Time
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT revoked_at FROM mcp_api_keys WHERE id = $1`, otherKeyID).Scan(&revokedAt))
	require.Nil(t, revokedAt, "foreign key must stay unrevoked")

	// Nonexistent id under own account → same single error shape.
	err = ks.RevokeForAccount(ctx, owner, uuid.New())
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found or already revoked")

	// Own key → revoked, denied on next verify.
	require.NoError(t, ks.RevokeForAccount(ctx, owner, ownerKeyID))
	if _, verr := verifyToken(t, ks, ownerTok); !errors.Is(verr, sdkauth.ErrInvalidToken) {
		t.Fatalf("own revoked key must be denied, got %v", verr)
	}

	// Re-revoke (idempotent no-op shape) → error again.
	require.Error(t, ks.RevokeForAccount(ctx, owner, ownerKeyID))
}
