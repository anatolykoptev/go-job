// mcpkeys.go — mcp_api_keys (migration 014): the per-account MCP bearer
// substrate (plan ADR-3). KeyStore is the single seam for everything that
// touches the table: the edge verifier mounted on both MCP listeners today,
// plus the mint/revoke/seed surface the gojob-admin CLI drives
// (p1-gojob-admin-cli) — the CLI gets real store methods, never raw SQL.
//
// Security shape, load-bearing:
//
//   - storage is sha256(raw token) only — a DB read never yields a usable
//     credential; key_prefix (first 8 chars) is the ONLY token fragment that
//     exists in the table or in logs;
//   - the verifier is STATELESS: one indexed lookup per request, so
//     revocation and account deactivation take effect on the very next call;
//   - last_used_at updates are throttled (>60s stale) and dispatched async —
//     the auth path never blocks on a write.
package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
)

// keyPrefixLen is the token-prefix width stored in key_prefix and logged on
// auth failures — enough to identify a key to an operator, never enough to
// authenticate.
const keyPrefixLen = 8

// KeyPrefix returns the public identifier fragment of a raw bearer token —
// the only part of a token that may appear in logs or the key_prefix column.
func KeyPrefix(token string) string {
	if len(token) > keyPrefixLen {
		return token[:keyPrefixLen]
	}
	return token
}

// lastUsedMinInterval throttles last_used_at writes: a key's timestamp is
// refreshed at most this often, so hot paths do not turn every request into a
// write.
const lastUsedMinInterval = time.Minute

// mintTokenLen is the entropy of generated keys (32 bytes ≈ 256 bits).
const mintTokenLen = 32

// KeyStore owns mcp_api_keys rows on the resume/hunt pool. Constructed on the
// DB-ready path in initEngine (nil when DATABASE_URL is absent) — a nil
// KeyStore means "no DB auth", never "no auth".
type KeyStore struct {
	pool *pgxpool.Pool
	now  func() time.Time // test seam for the last_used_at throttle window
	wg   sync.WaitGroup   // in-flight last_used_at writes; tests wait on it
}

// NewKeyStore wraps pool as the mcp_api_keys store.
func NewKeyStore(pool *pgxpool.Pool) *KeyStore {
	return &KeyStore{pool: pool, now: time.Now}
}

// keyLookupSQL resolves a presented bearer token (already sha256'd by the
// caller) to its owning ACTIVE account. The join is what makes account
// deactivation an immediate auth kill; revoked_at IS NULL does the same for
// key revocation.
const keyLookupSQL = `
SELECT k.id, k.account_id, k.last_used_at
FROM mcp_api_keys k
JOIN panel_accounts a ON a.id = k.account_id
WHERE k.key_hash = $1
  AND k.revoked_at IS NULL
  AND a.active`

// Verifier returns the stateless go-sdk TokenVerifier mounted on BearerAuth
// for both MCP listeners (:8891 edge and :8897 panelmcp). Each call is one
// indexed SELECT — revocation and deactivation apply on the next request.
//
// Failure semantics: an unknown/revoked/inactive key maps to
// sdkauth.ErrInvalidToken (→ 401); a store error propagates as-is (→ 500, and
// still denies). Only the token's KeyPrefix is ever logged — never the token.
func (k *KeyStore) Verifier() sdkauth.TokenVerifier {
	return func(ctx context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
		return k.verify(ctx, token)
	}
}

func (k *KeyStore) verify(ctx context.Context, token string) (*sdkauth.TokenInfo, error) {
	if token == "" {
		return nil, sdkauth.ErrInvalidToken
	}
	sum := sha256.Sum256([]byte(token))
	var keyID, accountID uuid.UUID
	var lastUsed *time.Time
	err := k.pool.QueryRow(ctx, keyLookupSQL, sum[:]).Scan(&keyID, &accountID, &lastUsed)
	if errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("mcp bearer: rejected key (unknown, revoked, or inactive account)",
			slog.String("key_prefix", KeyPrefix(token)))
		return nil, sdkauth.ErrInvalidToken
	}
	if err != nil {
		slog.Error("mcp bearer: key lookup failed",
			slog.String("key_prefix", KeyPrefix(token)), slog.Any("error", err))
		return nil, fmt.Errorf("mcp key lookup: %w", err)
	}
	// Non-empty, valid UserID is a hard contract (ADR-3): TokenInfo with an
	// empty/unparseable UserID would authenticate the request but fail
	// AccountFrom closed downstream — the account_id FK makes this defensive.
	if accountID == uuid.Nil {
		slog.Error("mcp bearer: key resolved to empty account id",
			slog.String("key_prefix", KeyPrefix(token)))
		return nil, sdkauth.ErrInvalidToken
	}
	if lastUsed == nil || k.now().Sub(*lastUsed) > lastUsedMinInterval {
		k.touchLastUsed(keyID)
	}
	// Expiration is required non-zero/in-the-future by the SDK's verify()
	// (auth.go: "token missing expiration" → 401). The value is cosmetic —
	// this verifier re-runs on every request, so it does not extend trust.
	return &sdkauth.TokenInfo{
		UserID:     accountID.String(),
		Expiration: k.now().Add(time.Hour),
	}, nil
}

// touchLastUsed refreshes last_used_at off the request path: a detached,
// deadline-bound context so client disconnects cannot cancel the write and a
// wedged pool cannot leak the goroutine. Failures are logged, never fatal —
// last_used_at is audit data, not auth data.
func (k *KeyStore) touchLastUsed(keyID uuid.UUID) {
	k.wg.Add(1)
	go func() {
		defer k.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := k.pool.Exec(ctx,
			`UPDATE mcp_api_keys SET last_used_at = now() WHERE id = $1`, keyID,
		); err != nil {
			slog.Warn("mcp bearer: last_used_at update failed", slog.Any("error", err))
		}
	}()
}

// Mint generates a fresh bearer token for accountID and stores its sha256 —
// the plaintext return value is shown to the operator ONCE (by the CLI) and
// is unrecoverable afterwards. The gj_ marker makes a leaked key grep-able
// without colliding with other secrets in the same file.
func (k *KeyStore) Mint(ctx context.Context, accountID uuid.UUID, label string) (string, error) {
	var raw [mintTokenLen]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("mcp key mint: %w", err)
	}
	token := "gj_" + base64.RawURLEncoding.EncodeToString(raw[:])
	sum := sha256.Sum256([]byte(token))
	if _, err := k.pool.Exec(ctx, `
		INSERT INTO mcp_api_keys (account_id, key_hash, key_prefix, label)
		VALUES ($1, $2, $3, $4)`,
		accountID, sum[:], KeyPrefix(token), label,
	); err != nil {
		return "", fmt.Errorf("mcp key mint insert: %w", err)
	}
	return token, nil
}

// Revoke sets revoked_at on keyID — the verifier's next lookup misses the row.
// Returns ErrAccountNotFound-style signal via pgx: zero rows affected means
// the key id did not exist (or was already revoked — idempotent for the CLI).
func (k *KeyStore) Revoke(ctx context.Context, keyID uuid.UUID) error {
	ct, err := k.pool.Exec(ctx,
		`UPDATE mcp_api_keys SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, keyID)
	if err != nil {
		return fmt.Errorf("mcp key revoke: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return errors.New("mcp key revoke: key not found or already revoked")
	}
	return nil
}

// SeedEdgeToken folds a pre-existing plaintext token (the live Caddy map
// token, delivered via MCP_LEGACY_TOKEN_SEED) into mcp_api_keys under
// accountID — the ADR-4 zero-window cutover: the DB verifier accepts the edge
// token from its first request, so the Caddy {mcp_valid} exemption never opens
// an auth gap. ON CONFLICT DO NOTHING makes it idempotent across restarts and
// against a CLI-minted row for the same token.
//
// Returns inserted=false when the key_hash already exists — the existing row
// (any owner/label) is authoritative and left untouched.
func (k *KeyStore) SeedEdgeToken(ctx context.Context, accountID uuid.UUID, rawToken, label string) (inserted bool, err error) {
	if rawToken == "" {
		return false, errors.New("mcp key seed: empty token")
	}
	sum := sha256.Sum256([]byte(rawToken))
	ct, err := k.pool.Exec(ctx, `
		INSERT INTO mcp_api_keys (account_id, key_hash, key_prefix, label)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (key_hash) DO NOTHING`,
		accountID, sum[:], KeyPrefix(rawToken), label,
	)
	if err != nil {
		return false, fmt.Errorf("mcp key seed: %w", err)
	}
	return ct.RowsAffected() == 1, nil
}
