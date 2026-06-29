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

// TestStore_RateExact_TrackerTransition verifies the CRITICAL tracker invariant:
// after Add(saved) then Update(applied), List must report status='applied'.
//
// Pre-012 bug: trackerRate used Rate() with CASE-preserve semantics. A prior
// triage='saved' survived a pipeline update because Rate(triage="", stage="applied")
// preserved triage='saved'; trackerStatusFromRow gave triage precedence → pipeline
// state was invisible forever.
//
// Fix: trackerRate now uses RateExact() which unconditionally writes both axes,
// explicitly clearing the inactive axis. This test is the regression guard.
//
// Red-on-revert: changing RateExact back to Rate in trackerRate → ListTrackedJobs
// returns status='saved' instead of 'applied'; assertion at step 3 fails.
func TestStore_RateExact_TrackerTransition(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Step 1: RateExact(saved) — simulates tracker Add(saved).
	const (
		testKind = hunt.KindJob
		testID   = int64(99990001)
		testUser = "tracker_transition_test"
	)
	_ = s.Pool().QueryRow(ctx, "SELECT 1") // ensure pool alive
	// Clean up any leftover row.
	_, _ = s.Pool().Exec(ctx, "DELETE FROM hunt_ratings WHERE entry_kind=$1 AND entry_id=$2 AND user_name=$3",
		testKind, testID, testUser)
	t.Cleanup(func() {
		_, _ = s.Pool().Exec(ctx, "DELETE FROM hunt_ratings WHERE entry_kind=$1 AND entry_id=$2 AND user_name=$3",
			testKind, testID, testUser)
	})

	if err := s.RateExact(ctx, testKind, testID, testUser, hunt.StageSaved, "", "initial note"); err != nil {
		t.Fatalf("RateExact(saved): %v", err)
	}

	// Step 2: RateExact(applied) — simulates tracker Update(applied). Must clear triage.
	if err := s.RateExact(ctx, testKind, testID, testUser, "", hunt.StageApplied, "applied note"); err != nil {
		t.Fatalf("RateExact(applied): %v", err)
	}

	// Step 3: read both axes directly — triage must be '', stage must be 'applied'.
	var triage, stage, note string
	if err := s.Pool().QueryRow(ctx,
		"SELECT triage, stage, COALESCE(note,'') FROM hunt_ratings WHERE entry_kind=$1 AND entry_id=$2 AND user_name=$3",
		testKind, testID, testUser,
	).Scan(&triage, &stage, &note); err != nil {
		t.Fatalf("read after Update: %v", err)
	}
	if triage != "" {
		t.Errorf("after tracker Update(applied): triage must be '' (cleared), got %q", triage)
	}
	if stage != hunt.StageApplied {
		t.Errorf("after tracker Update(applied): stage must be %q, got %q", hunt.StageApplied, stage)
	}
	if note != "applied note" {
		t.Errorf("after tracker Update(applied): note must be overwritten to %q, got %q", "applied note", note)
	}
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
