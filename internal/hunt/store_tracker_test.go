package hunt_test

import (
	"context"
	"os"
	"testing"

	"github.com/anatolykoptev/go_job/internal/dbtest"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/jackc/pgx/v5/pgxpool"
)

func setupTestStore(t *testing.T) *hunt.Store {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	dbtest.RequireTestDB(t, dsn)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	return hunt.NewStore(pool)
}

func TestStore_ListTrackedJobs_Empty(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	rows, total, err := store.ListTrackedJobs(ctx, hunt.TrackedFilter{
		User:  "nonexistent-user-xyz-test",
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 0 {
		t.Errorf("expected total=0, got %d", total)
	}
	if len(rows) != 0 {
		t.Errorf("expected empty rows, got %d", len(rows))
	}
}
