package accounts

// In-package tests: the last_used_at throttle is verified against KeyStore's
// internal clock seam (now) and write waitgroup (wg) — deterministic, no
// sleeps, no Eventually races on a loaded box.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/dbtest"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func openInternalKeyStore(t *testing.T) (*KeyStore, *pgxpool.Pool, uuid.UUID) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	dbtest.RequireTestDB(t, dsn)
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	ctx := context.Background()
	// Drop the identity tables AND clear 014's schema_migrations row —
	// pgutil skips already-recorded files, so without the delete Migrate
	// would never recreate mcp_api_keys after the drop.
	_, err = pool.Exec(ctx,
		`DROP TABLE IF EXISTS mcp_api_keys;
		 DROP TABLE IF EXISTS panel_totp_recovery_codes;
		 DROP TABLE IF EXISTS panel_accounts;
		 DO $$
		 BEGIN
		     IF to_regclass('public.schema_migrations') IS NOT NULL THEN
		         DELETE FROM schema_migrations WHERE name = '014_mcp_api_keys.sql';
		     END IF;
		 END $$`)
	require.NoError(t, err)
	_, op, err := Bootstrap(ctx, pool, OperatorSeed{Email: "throttle@t.example", Password: "pw-123456"})
	require.NoError(t, err)
	require.NoError(t, hunt.NewStore(pool).Migrate(ctx))
	opID, err := uuid.Parse(op.ID)
	require.NoError(t, err)
	return NewKeyStore(pool), pool, opID
}

func lastUsedAt(t *testing.T, pool *pgxpool.Pool, keyID uuid.UUID) *time.Time {
	t.Helper()
	var ts *time.Time
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT last_used_at FROM mcp_api_keys WHERE id = $1`, keyID).Scan(&ts))
	return ts
}

// TestKeyStore_LastUsedThrottle pins the write-path contract: the first
// verify stamps last_used_at; a verify inside the 60s window does NOT write
// again; a key whose stamp has aged past the window writes once more. The
// update rides k.wg off the request path — the auth decision never waits on
// the write.
func TestKeyStore_LastUsedThrottle(t *testing.T) {
	ks, pool, opID := openInternalKeyStore(t)
	ctx := context.Background()

	token, err := ks.Mint(ctx, opID, "throttle")
	require.NoError(t, err)
	var keyID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM mcp_api_keys WHERE key_prefix = $1`, KeyPrefix(token)).Scan(&keyID))

	// First verify: last_used_at NULL → write dispatched.
	_, err = ks.verify(ctx, token)
	require.NoError(t, err)
	ks.wg.Wait()
	first := lastUsedAt(t, pool, keyID)
	require.NotNil(t, first, "first verify must stamp last_used_at")

	// Second verify inside the window: no write — timestamp unchanged.
	_, err = ks.verify(ctx, token)
	require.NoError(t, err)
	ks.wg.Wait()
	require.Equal(t, *first, *lastUsedAt(t, pool, keyID),
		"verify inside lastUsedMinInterval must not rewrite last_used_at")

	// Age the stamp past the window and verify again — write fires once more.
	_, err = pool.Exec(ctx,
		`UPDATE mcp_api_keys SET last_used_at = now() - interval '61 seconds' WHERE id = $1`, keyID)
	require.NoError(t, err)
	_, err = ks.verify(ctx, token)
	require.NoError(t, err)
	ks.wg.Wait()
	require.True(t, lastUsedAt(t, pool, keyID).After(*first),
		"stale stamp must refresh on the next verify")
}
