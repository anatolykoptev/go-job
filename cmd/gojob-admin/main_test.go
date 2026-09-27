package main

// DB-backed tests for the gojob-admin CLI (plan ADR-12). Every test drives the
// real run() entrypoint against a fresh schema — openPool drops the identity
// tables first, so run's embedded accounts.Bootstrap recreates them exactly as
// a first-run on a fresh deploy would.

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/anatolykoptev/go_job/internal/accounts"
	"github.com/anatolykoptev/go_job/internal/dbtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/stretchr/testify/require"
)

// openPool returns a pool on the ephemeral *_test database with the identity
// tables dropped — the absent-table state run()'s Bootstrap must recover from.
func openPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	dbtest.RequireTestDB(t, dsn)
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	dbtest.DropAccountTables(t, pool)
	return pool
}

// runCLI invokes run() with captured streams — stdout stays machine-parseable
// (ids, tokens, tables); warnings go to stderr.
func runCLI(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err = run(context.Background(), &out, &errOut, args)
	return out.String(), errOut.String(), err
}

// fieldLine extracts the value of a "key: value" output line.
func fieldLine(t *testing.T, output, key string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `:\s*(\S+)\s*$`)
	m := re.FindStringSubmatch(output)
	require.NotNil(t, m, "output must contain a %q line, got:\n%s", key+":", output)
	return m[1]
}

func mintKey(t *testing.T, accountRef string) (token string) {
	t.Helper()
	out, _, err := runCLI(t, "key", "mint", "--account", accountRef, "--label", "t-key")
	require.NoError(t, err)
	return fieldLine(t, out, "token")
}

func verifyKey(t *testing.T, ks *accounts.KeyStore, token string) error {
	t.Helper()
	_, err := ks.Verifier()(context.Background(), token,
		httptest.NewRequest(http.MethodGet, "/mcp", nil))
	return err
}

// TestAccount_CreateThenList: create→list round-trip on a fresh DB (Bootstrap
// inside run() provisions panel_accounts), default role 'user', and the list
// output never carries credential material.
func TestAccount_CreateThenList(t *testing.T) {
	pool := openPool(t)

	out, _, err := runCLI(t, "account", "create",
		"--email", "roundtrip@t.dev", "--name", "Round Trip", "--password", "s3cret-pw")
	require.NoError(t, err)
	id := fieldLine(t, out, "id")
	_, perr := uuid.Parse(id)
	require.NoError(t, perr, "create must print a parseable account id")

	out, _, err = runCLI(t, "account", "list")
	require.NoError(t, err)
	require.Contains(t, out, "roundtrip@t.dev")
	require.Contains(t, out, "Round Trip")
	require.Contains(t, out, id)
	require.Contains(t, out, "user", "default role must be 'user'")

	// The stored bcrypt hash must never appear in list output.
	var hash string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT password_hash FROM panel_accounts WHERE email = 'roundtrip@t.dev'`).Scan(&hash))
	require.NotEmpty(t, hash)
	require.NotContains(t, out, hash, "account list must never print password_hash")
}

// TestAccount_DeactivateDeniesKeys: mint a key, verify it authenticates, then
// deactivate the account — the next verifier call on the same token must deny
// (the keyLookupSQL join makes deactivation immediate).
func TestAccount_DeactivateDeniesKeys(t *testing.T) {
	pool := openPool(t)
	ks := accounts.NewKeyStore(pool)

	_, _, err := runCLI(t, "account", "create", "--email", "victim@t.dev", "--name", "V")
	require.NoError(t, err)
	token := mintKey(t, "victim@t.dev")

	require.NoError(t, verifyKey(t, ks, token), "minted key must verify before deactivation")

	_, _, err = runCLI(t, "account", "deactivate", "--email", "victim@t.dev")
	require.NoError(t, err)

	if err := verifyKey(t, ks, token); !errors.Is(err, sdkauth.ErrInvalidToken) {
		t.Fatalf("deactivated account's key must fail verification, got %v", err)
	}
}

// TestKey_MintListRevoke: the minted token is printed once on stdout, key list
// shows prefix/label but NEVER the token or its hash, and revoke kills the key
// on the very next verification.
func TestKey_MintListRevoke(t *testing.T) {
	pool := openPool(t)
	ks := accounts.NewKeyStore(pool)

	_, _, err := runCLI(t, "account", "create", "--email", "keyops@t.dev", "--name", "K")
	require.NoError(t, err)
	token := mintKey(t, "keyops@t.dev")
	require.True(t, strings.HasPrefix(token, "gj_"), "minted tokens carry the gj_ marker")

	out, _, err := runCLI(t, "key", "list", "--account", "keyops@t.dev")
	require.NoError(t, err)
	require.Contains(t, out, accounts.KeyPrefix(token), "list must show the key prefix")
	require.Contains(t, out, "t-key")
	require.NotContains(t, out, token, "key list must never print the raw token")

	// revoke by prefix: single active match → succeeds.
	_, _, err = runCLI(t, "key", "revoke", "--prefix", accounts.KeyPrefix(token))
	require.NoError(t, err)

	if err := verifyKey(t, ks, token); !errors.Is(err, sdkauth.ErrInvalidToken) {
		t.Fatalf("revoked key must fail verification, got %v", err)
	}
}

// TestKey_RevokePrefixAmbiguity: two active keys sharing a key_prefix must make
// revoke --prefix refuse — an operator picking the wrong key is worse than a
// second command.
func TestKey_RevokePrefixAmbiguity(t *testing.T) {
	pool := openPool(t)
	ctx := context.Background()
	ks := accounts.NewKeyStore(pool)

	_, _, err := runCLI(t, "account", "create", "--email", "amb@t.dev", "--name", "A")
	require.NoError(t, err)

	// Two seeded keys sharing the first-8 prefix (Mint can't force a
	// collision — SeedEdgeToken takes caller-supplied tokens).
	id, err := accounts.ResolveAccountID(ctx, pool, "amb@t.dev")
	require.NoError(t, err)
	tokA := "gj_ambig_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tokB := "gj_ambig_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	_, err = ks.SeedEdgeToken(ctx, id, tokA, "a")
	require.NoError(t, err)
	_, err = ks.SeedEdgeToken(ctx, id, tokB, "b")
	require.NoError(t, err)

	_, _, err = runCLI(t, "key", "revoke", "--prefix", "gj_ambig")
	require.Error(t, err, "ambiguous prefix must be refused")
	require.Contains(t, err.Error(), "ambiguous")

	// Both keys must still verify — the refusal revoked nothing.
	require.NoError(t, verifyKey(t, ks, tokA))
	require.NoError(t, verifyKey(t, ks, tokB))
}

// TestAccount_CreateOwnerRoleRejected (fitness, spec): the CLI offers no code
// path to role='owner' — the flag validator rejects it before any INSERT, so
// no row is created. The DB CHECK is the second wall (accounts_test covers it).
func TestAccount_CreateOwnerRoleRejected(t *testing.T) {
	openPool(t)

	_, _, err := runCLI(t, "account", "create",
		"--email", "boss@t.dev", "--name", "Boss", "--role", "owner")
	require.Error(t, err, "role owner must be rejected")
	require.Contains(t, err.Error(), "role")

	// The reject precedes Bootstrap, so the count runs through a real command
	// (account list) — this also proves the rejection left no partial schema.
	out, _, err := runCLI(t, "account", "list")
	require.NoError(t, err)
	require.NotContains(t, out, "boss@t.dev", "a rejected role must not leave a created row")
}

// TestAccount_CreateNotifyChatIDStored: since P3 Bootstrap creates
// account_hunt_settings, --notify-chat-id writes the new account's settings
// row directly — enabled=false (provisioning never silently arms a hunt,
// ADR-7/ADR-12). The post-P3 warn path stays for a deployment where the table
// was dropped out from under Bootstrap.
func TestAccount_CreateNotifyChatIDStored(t *testing.T) {
	pool := openPool(t)

	out, errOut, err := runCLI(t, "account", "create",
		"--email", "notify@t.dev", "--name", "N", "--notify-chat-id", "-1001234")
	require.NoError(t, err)
	require.NotContains(t, errOut, "hunt settings table not yet present",
		"the table exists post-P3 — the flag stores, no warn")
	id := fieldLine(t, out, "id")

	var chatID int64
	var enabled bool
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT notify_chat_id, enabled FROM account_hunt_settings WHERE account_id = $1::uuid`,
		id).Scan(&chatID, &enabled))
	require.Equal(t, int64(-1001234), chatID, "notify_chat_id stored on the account row")
	require.False(t, enabled, "provisioned settings row must stay disabled")
}
