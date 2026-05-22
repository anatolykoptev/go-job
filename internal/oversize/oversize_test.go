package oversize

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testPool creates a pgxpool for integration tests and skips if DATABASE_URL is not set.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping store integration tests")
	}
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

	id, err := store.Save(ctx, entry)
	require.NoError(t, err)
	assert.Greater(t, id, int64(0))

	got, err := store.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, id, got.ID)
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

	_, err := store.Get(ctx, 9_999_999)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestStore_List_FilterByTool(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	ctx := context.Background()
	require.NoError(t, store.Migrate(ctx))
	truncate(t, pool)

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
		_, err := store.Save(ctx, makeEntry("tool_a"))
		require.NoError(t, err)
	}
	for range 2 {
		_, err := store.Save(ctx, makeEntry("tool_b"))
		require.NoError(t, err)
	}

	listA, err := store.List(ctx, ListFilter{ToolName: "tool_a"})
	require.NoError(t, err)
	assert.Len(t, listA, 3)
	for _, e := range listA {
		assert.Equal(t, "tool_a", e.ToolName)
	}

	listB, err := store.List(ctx, ListFilter{ToolName: "tool_b"})
	require.NoError(t, err)
	assert.Len(t, listB, 2)
}

func TestStore_Purge(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	ctx := context.Background()
	require.NoError(t, store.Migrate(ctx))
	truncate(t, pool)

	p := json.RawMessage(`{}`)
	for range 3 {
		_, err := store.Save(ctx, Entry{
			ToolName:  "purge_tool",
			Payload:   p,
			SizeBytes: 2,
			SHA256:    "yy",
		})
		require.NoError(t, err)
	}

	// Purge everything created before now+1s
	deleted, err := store.Purge(ctx, time.Now().Add(time.Second))
	require.NoError(t, err)
	assert.Equal(t, int64(3), deleted)

	remaining, err := store.List(ctx, ListFilter{ToolName: "purge_tool"})
	require.NoError(t, err)
	assert.Len(t, remaining, 0)
}
