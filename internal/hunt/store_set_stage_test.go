package hunt_test

import (
	"context"
	"testing"

	"github.com/anatolykoptev/go_job/internal/hunt"
)

// TestStore_SetStage_SetsStage verifies that SetStage upserts the stage on a
// fresh row (no prior rating). Red-on-revert: removing SetStage → compile error.
func TestStore_SetStage_SetsStage(t *testing.T) {
	s, close := migratedStore(t)
	defer close()
	id := insertStarTestJob(t, s)

	if err := s.SetStage(context.Background(), "job", id, starTestUser, hunt.StageApplied); err != nil {
		t.Fatalf("SetStage: %v", err)
	}
	if got := readStage(t, s, id); got != hunt.StageApplied {
		t.Errorf("SetStage: want stage=%q, got %q", hunt.StageApplied, got)
	}
}

// TestStore_SetStage_PreservesNote verifies the note-preservation contract:
// SetStage must NOT wipe an existing note written by Rate.
// Red-on-revert: adding note to the ON CONFLICT SET clause → note wiped → fails.
func TestStore_SetStage_PreservesNote(t *testing.T) {
	s, close := migratedStore(t)
	defer close()
	id := insertStarTestJob(t, s)
	const wantNote = "this note must survive stage change"

	// Seed a row with a note via Rate (the detail-page write path).
	// StageInteresting is a triage-axis value; stage="".
	if err := s.Rate(context.Background(), "job", id, starTestUser, hunt.StageInteresting, "", wantNote); err != nil {
		t.Fatalf("Rate (seed): %v", err)
	}
	// Now change stage via SetStage (the inline-dropdown write path).
	if err := s.SetStage(context.Background(), "job", id, starTestUser, hunt.StageApplied); err != nil {
		t.Fatalf("SetStage: %v", err)
	}
	// Stage must have changed.
	if got := readStage(t, s, id); got != hunt.StageApplied {
		t.Errorf("SetStage: want stage=%q, got %q", hunt.StageApplied, got)
	}
	// Note must be preserved — read it directly.
	pool := s.Pool()
	var note *string
	if err := pool.QueryRow(context.Background(),
		`SELECT note FROM hunt_ratings WHERE entry_kind='job' AND entry_id=$1 AND user_name=$2`,
		id, starTestUser,
	).Scan(&note); err != nil {
		t.Fatalf("read note: %v", err)
	}
	if note == nil || *note != wantNote {
		t.Errorf("SetStage must preserve note: want %q, got %v", wantNote, note)
	}
}

// TestStore_SetStage_UpdatesStage verifies that calling SetStage twice changes
// the stage both times (idempotent upsert).
// Red-on-revert: ON CONFLICT DO NOTHING would prevent the update → fails.
func TestStore_SetStage_UpdatesStage(t *testing.T) {
	s, close := migratedStore(t)
	defer close()
	id := insertStarTestJob(t, s)

	if err := s.SetStage(context.Background(), "job", id, starTestUser, hunt.StageApplied); err != nil {
		t.Fatalf("SetStage first: %v", err)
	}
	if err := s.SetStage(context.Background(), "job", id, starTestUser, hunt.StageInterview); err != nil {
		t.Fatalf("SetStage second: %v", err)
	}
	if got := readStage(t, s, id); got != hunt.StageInterview {
		t.Errorf("SetStage second update: want stage=%q, got %q", hunt.StageInterview, got)
	}
}
