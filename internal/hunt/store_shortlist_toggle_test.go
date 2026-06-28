package hunt_test

import (
	"context"
	"testing"

	"github.com/anatolykoptev/go_job/internal/hunt"
)

// insertMinimalJob inserts a bare-minimum hunt_jobs row for toggle tests.
func insertMinimalJob(t *testing.T, s *hunt.Store) int64 {
	t.Helper()
	j := hunt.Job{
		DedupHash: hunt.DedupHash("https://toggle-test.example/job"),
		Title:     "Toggle Test Role",
		Company:   "ToggleCo",
		URL:       "https://toggle-test.example/job",
		Source:    "toggle_test",
		Status:    "open",
	}
	id, _, err := s.UpsertJob(context.Background(), j)
	if err != nil {
		t.Fatalf("insertMinimalJob: %v", err)
	}
	t.Cleanup(func() {
		// Best-effort cleanup — no error check.
		_, _ = s.Pool().Exec(context.Background(), "DELETE FROM hunt_jobs WHERE source = 'toggle_test'")
	})
	return id
}

// TestStore_ToggleShortlist verifies:
//  1. Default state is false (shortlisted NOT NULL DEFAULT false).
//  2. First toggle flips to true.
//  3. Second toggle flips back to false (idempotent round-trip).
//
// Red-on-revert: removing ToggleShortlist or the shortlisted column → compile/SQL error.
// Red-on-wrong-logic: single-flip returning false, or double-flip not returning false.
func TestStore_ToggleShortlist(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()

	s := hunt.NewStore(pool)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	id := insertMinimalJob(t, s)

	// Verify default shortlisted = false before any toggle.
	var initial bool
	if err := pool.QueryRow(ctx, "SELECT shortlisted FROM hunt_jobs WHERE id = $1", id).Scan(&initial); err != nil {
		t.Fatalf("read initial shortlisted: %v", err)
	}
	if initial {
		t.Fatalf("initial shortlisted: want false, got true")
	}

	// First toggle: false → true.
	got, err := s.ToggleShortlist(ctx, id)
	if err != nil {
		t.Fatalf("ToggleShortlist (1st): %v", err)
	}
	if !got {
		t.Errorf("ToggleShortlist (1st): want true, got false")
	}

	// Verify DB persisted true.
	var afterFirst bool
	if err := pool.QueryRow(ctx, "SELECT shortlisted FROM hunt_jobs WHERE id = $1", id).Scan(&afterFirst); err != nil {
		t.Fatalf("read after 1st toggle: %v", err)
	}
	if !afterFirst {
		t.Errorf("DB shortlisted after 1st toggle: want true, got false")
	}

	// Second toggle: true → false (round-trip).
	got2, err := s.ToggleShortlist(ctx, id)
	if err != nil {
		t.Fatalf("ToggleShortlist (2nd): %v", err)
	}
	if got2 {
		t.Errorf("ToggleShortlist (2nd): want false, got true")
	}

	// Verify DB persisted false.
	var afterSecond bool
	if err := pool.QueryRow(ctx, "SELECT shortlisted FROM hunt_jobs WHERE id = $1", id).Scan(&afterSecond); err != nil {
		t.Fatalf("read after 2nd toggle: %v", err)
	}
	if afterSecond {
		t.Errorf("DB shortlisted after 2nd toggle: want false, got true")
	}
}
