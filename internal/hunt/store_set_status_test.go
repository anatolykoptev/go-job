package hunt_test

import (
	"context"
	"errors"
	"testing"

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
