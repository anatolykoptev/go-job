package oversize

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/accounts"
	"github.com/anatolykoptev/go_job/internal/dbtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testPool creates a pgxpool for integration tests; skips if DATABASE_URL unset;
// fatals if it points at a non-_test database.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	dbtest.RequireTestDB(t, dsn)
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })
	return pool
}

// truncate clears the test table between test runs.
func truncate(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), "TRUNCATE oversize_responses RESTART IDENTITY")
	require.NoError(t, err)
}

// testAccount bootstraps the accounts substrate (panel_accounts + the
// oversize account FK via EnsureOversizeAccountScope) and returns a fresh
// account UUID — scoped writes need a real account: a random UUID would
// violate the FK, and uuid.Nil is refused by the facade itself.
func testAccount(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	_, _, err := accounts.Bootstrap(ctx, pool, accounts.OperatorSeed{})
	require.NoError(t, err, "accounts.Bootstrap")
	aid, _, err := accounts.CreateAccount(ctx, pool,
		"oversize-test-"+uuid.NewString()[:12]+"@example.com", "oversize test", nil, "user")
	require.NoError(t, err, "accounts.CreateAccount")
	return aid
}

func TestStore_Migrate_Idempotent(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	ctx := context.Background()

	// First run
	err := store.Migrate(ctx)
	require.NoError(t, err)

	// Second run — must not error (IF NOT EXISTS on table + indexes)
	err = store.Migrate(ctx)
	require.NoError(t, err)

	// Table must exist
	var exists bool
	err = pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'oversize_responses'
		)
	`).Scan(&exists)
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestStore_SaveGet_RoundTrip(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	ctx := context.Background()
	require.NoError(t, store.Migrate(ctx))
	truncate(t, pool)
	aid := testAccount(t, pool)
	astore := store.ForAccount(aid)

	payload := json.RawMessage(`{"jobs":[{"id":1,"title":"Go Engineer"}]}`)
	sample := json.RawMessage(`[{"id":1}]`)
	entry := Entry{
		ToolName:  "job_search",
		QueryHash: "abc123",
		Payload:   payload,
		SizeBytes: len(payload),
		SHA256:    "deadbeef",
		Sample:    sample,
		ItemCount: 1,
	}

	id, err := astore.Save(ctx, entry)
	require.NoError(t, err)
	assert.Greater(t, id, int64(0))

	got, err := astore.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, id, got.ID)
	assert.Equal(t, aid, got.AccountID)
	assert.Equal(t, "job_search", got.ToolName)
	assert.Equal(t, "abc123", got.QueryHash)
	assert.Equal(t, len(payload), got.SizeBytes)
	assert.Equal(t, "deadbeef", got.SHA256)
	assert.Equal(t, 1, got.ItemCount)
	assert.JSONEq(t, string(payload), string(got.Payload))
}

func TestStore_Get_NotFound(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	ctx := context.Background()
	require.NoError(t, store.Migrate(ctx))
	aid := testAccount(t, pool)

	_, err := store.ForAccount(aid).Get(ctx, 9_999_999)
	require.ErrorIs(t, err, ErrNotFound)
}

// TestStore_AccountIsolation is the oversize leg of the P4 deny matrix
// (plan ADR-10): account B cannot read, list, or purge account A's rows, and
// a Nil-account view fails closed on write. RED-on-revert: dropping
// `AND account_id = $N` from AccountStore.Get returns A's row to B.
func TestStore_AccountIsolation(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	ctx := context.Background()
	require.NoError(t, store.Migrate(ctx))
	truncate(t, pool)
	aidA := testAccount(t, pool)
	aidB := testAccount(t, pool)

	id, err := store.ForAccount(aidA).Save(ctx, Entry{
		ToolName: "iso_tool", Payload: json.RawMessage(`{"x":1}`), SizeBytes: 7, SHA256: "h",
	})
	require.NoError(t, err)

	// B cannot read A's row — foreign ids read as absent.
	_, err = store.ForAccount(aidB).Get(ctx, id)
	require.ErrorIs(t, err, ErrNotFound)

	// B's list never carries A's rows.
	listB, err := store.ForAccount(aidB).List(ctx, ListFilter{})
	require.NoError(t, err)
	for _, e := range listB {
		assert.NotEqual(t, id, e.ID, "B's list must never contain A's row")
	}
	listA, err := store.ForAccount(aidA).List(ctx, ListFilter{})
	require.NoError(t, err)
	require.Len(t, listA, 1)

	// B's purge cannot touch A's rows.
	n, err := store.ForAccount(aidB).Purge(ctx, time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Zero(t, n)
	_, err = store.ForAccount(aidA).Get(ctx, id)
	require.NoError(t, err, "A's row survives B's purge")

	// uuid.Nil fails closed: writes refuse, reads match nothing.
	nilStore := store.ForAccount(uuid.Nil)
	_, err = nilStore.Save(ctx, Entry{ToolName: "x", Payload: json.RawMessage(`{}`), SizeBytes: 2})
	require.ErrorIs(t, err, errNoAccountScope)
	_, err = nilStore.Get(ctx, id)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestStore_List_FilterByTool(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	ctx := context.Background()
	require.NoError(t, store.Migrate(ctx))
	truncate(t, pool)
	aid := testAccount(t, pool)
	astore := store.ForAccount(aid)

	makeEntry := func(tool string) Entry {
		p := json.RawMessage(`{}`)
		return Entry{
			ToolName:  tool,
			Payload:   p,
			SizeBytes: 2,
			SHA256:    "xx",
		}
	}

	for range 3 {
		_, err := astore.Save(ctx, makeEntry("tool_a"))
		require.NoError(t, err)
	}
	for range 2 {
		_, err := astore.Save(ctx, makeEntry("tool_b"))
		require.NoError(t, err)
	}

	listA, err := astore.List(ctx, ListFilter{ToolName: "tool_a"})
	require.NoError(t, err)
	assert.Len(t, listA, 3)
	for _, e := range listA {
		assert.Equal(t, "tool_a", e.ToolName)
	}

	listB, err := astore.List(ctx, ListFilter{ToolName: "tool_b"})
	require.NoError(t, err)
	assert.Len(t, listB, 2)
}

func TestStore_Purge(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	ctx := context.Background()
	require.NoError(t, store.Migrate(ctx))
	truncate(t, pool)
	aid := testAccount(t, pool)
	astore := store.ForAccount(aid)

	p := json.RawMessage(`{}`)
	for range 3 {
		_, err := astore.Save(ctx, Entry{
			ToolName:  "purge_tool",
			Payload:   p,
			SizeBytes: 2,
			SHA256:    "yy",
		})
		require.NoError(t, err)
	}

	// Purge everything created before now+1s
	deleted, err := astore.Purge(ctx, time.Now().Add(time.Second))
	require.NoError(t, err)
	assert.Equal(t, int64(3), deleted)

	remaining, err := astore.List(ctx, ListFilter{ToolName: "purge_tool"})
	require.NoError(t, err)
	assert.Len(t, remaining, 0)
}
