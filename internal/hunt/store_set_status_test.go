package hunt_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/hunt"
)

// TestStore_SetStatus_UpdatesStatus verifies SetStatus changes hunt_jobs.status
// on an existing row and returns nil.
// Red-on-revert: removing SetStatus → compile error.
func TestStore_SetStatus_UpdatesStatus(t *testing.T) {
	s, close := migratedStore(t)
	defer close()
	id := insertStarTestJob(t, s) // inserts with status="open"

	if err := s.SetStatus(context.Background(), id, hunt.StatusClosed); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}

	// Read back and assert.
	pool := s.Pool()
	var got string
	if err := pool.QueryRow(context.Background(),
		`SELECT status FROM hunt_jobs WHERE id=$1`, id,
	).Scan(&got); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if got != hunt.StatusClosed {
		t.Errorf("SetStatus: want status=%q, got %q", hunt.StatusClosed, got)
	}
}

// TestStore_SetStatus_UpdatesMultipleTimes verifies SetStatus can be called
// repeatedly and each call persists the new value.
// Red-on-revert: ON CONFLICT DO NOTHING style guard would prevent second write → fails.
func TestStore_SetStatus_UpdatesMultipleTimes(t *testing.T) {
	s, close := migratedStore(t)
	defer close()
	id := insertStarTestJob(t, s)

	for _, status := range []string{hunt.StatusClosed, hunt.StatusMerged, hunt.StatusOpen} {
		if err := s.SetStatus(context.Background(), id, status); err != nil {
			t.Fatalf("SetStatus(%q): %v", status, err)
		}
		var got string
		if err := s.Pool().QueryRow(context.Background(),
			`SELECT status FROM hunt_jobs WHERE id=$1`, id,
		).Scan(&got); err != nil {
			t.Fatalf("read status after SetStatus(%q): %v", status, err)
		}
		if got != status {
			t.Errorf("SetStatus: want %q, got %q", status, got)
		}
	}
}

// TestStore_SetStatus_ErrNotFoundOnUnknownID verifies SetStatus returns ErrNotFound
// (wrapping hunt.ErrNotFound) when the id does not exist in hunt_jobs.
// Red-on-revert: removing the RowsAffected==0 check → silently returns nil.
func TestStore_SetStatus_ErrNotFoundOnUnknownID(t *testing.T) {
	s, close := migratedStore(t)
	defer close()

	const nonExistentID = int64(999999999)
	err := s.SetStatus(context.Background(), nonExistentID, hunt.StatusClosed)
	if err == nil {
		t.Fatal("SetStatus: want error for unknown id, got nil")
	}
	if !errors.Is(err, hunt.ErrNotFound) {
		t.Errorf("SetStatus: want errors.Is(err, ErrNotFound), got: %v", err)
	}
}

// TestStore_SetStatus_ClosedAt_Lockstep verifies closed_at stays in sync with status:
//   - closing (terminal status) stamps closed_at=NOW() when previously NULL.
//   - reopening (open) clears closed_at back to NULL.
//   - closing again after reopen re-stamps closed_at.
//
// Red-on-revert: removing the CASE expression from SetStatus → closed_at stays NULL
// on close, or stays set on reopen; either assertion below fails.
func TestStore_SetStatus_ClosedAt_Lockstep(t *testing.T) {
	s, close := migratedStore(t)
	defer close()
	id := insertStarTestJob(t, s) // inserts open, closed_at=NULL

	pool := s.Pool()
	readClosedAt := func() *time.Time {
		t.Helper()
		var ca *time.Time
		if err := pool.QueryRow(context.Background(),
			`SELECT closed_at FROM hunt_jobs WHERE id=$1`, id,
		).Scan(&ca); err != nil {
			t.Fatalf("read closed_at: %v", err)
		}
		return ca
	}

	// Initial state: closed_at must be NULL.
	if got := readClosedAt(); got != nil {
		t.Fatalf("initial closed_at: want nil, got %v", got)
	}

	// Close: closed_at must be stamped.
	before := time.Now()
	if err := s.SetStatus(context.Background(), id, hunt.StatusClosed); err != nil {
		t.Fatalf("SetStatus(closed): %v", err)
	}
	closedAt := readClosedAt()
	if closedAt == nil {
		t.Fatal("SetStatus(closed): want closed_at set, got nil")
	}
	if closedAt.Before(before) {
		t.Errorf("SetStatus(closed): closed_at %v is before NOW() call %v", closedAt, before)
	}

	// Second close: closed_at must NOT change (do not overwrite existing timestamp).
	firstClosedAt := *closedAt
	if err := s.SetStatus(context.Background(), id, hunt.StatusMerged); err != nil {
		t.Fatalf("SetStatus(merged): %v", err)
	}
	closedAt2 := readClosedAt()
	if closedAt2 == nil {
		t.Fatal("SetStatus(merged): closed_at must still be set after second terminal write")
	}
	if !closedAt2.Equal(firstClosedAt) {
		t.Errorf("SetStatus(merged): want closed_at unchanged (%v), got %v", firstClosedAt, *closedAt2)
	}

	// Reopen: closed_at must be cleared.
	if err := s.SetStatus(context.Background(), id, hunt.StatusOpen); err != nil {
		t.Fatalf("SetStatus(open): %v", err)
	}
	if got := readClosedAt(); got != nil {
		t.Errorf("SetStatus(open): want closed_at=NULL after reopen, got %v", got)
	}

	// Re-close after reopen: closed_at must be re-stamped.
	if err := s.SetStatus(context.Background(), id, hunt.StatusArchived); err != nil {
		t.Fatalf("SetStatus(archived): %v", err)
	}
	if got := readClosedAt(); got == nil {
		t.Error("SetStatus(archived after reopen): want closed_at set again, got nil")
	}
}
