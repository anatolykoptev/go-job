package hunt_test

import (
	"context"
	"testing"

	"github.com/anatolykoptev/go_job/internal/hunt"
)

// insertStarTestJob inserts a bare-minimum hunt_jobs row for star toggle tests.
func insertStarTestJob(t *testing.T, s *hunt.Store) int64 {
	t.Helper()
	j := hunt.Job{
		DedupHash: hunt.DedupHash("https://star-test.example/job"),
		Title:     "Star Test Role",
		Company:   "StarCo",
		URL:       "https://star-test.example/job",
		Source:    "star_test",
		Status:    "open",
	}
	id, _, err := s.UpsertJob(context.Background(), j)
	if err != nil {
		t.Fatalf("insertStarTestJob: %v", err)
	}
	t.Cleanup(func() {
		pool := s.Pool()
		_, _ = pool.Exec(context.Background(),
			"DELETE FROM hunt_ratings WHERE entry_kind='job' AND entry_id=$1", id)
		_, _ = pool.Exec(context.Background(),
			"DELETE FROM hunt_jobs WHERE id=$1", id)
	})
	return id
}

// activeStages mirrors shortlistActiveStages from adminui.
var activeStages = []string{
	hunt.StageInteresting,
	hunt.StageSaved,
	hunt.StageClaimed,
	hunt.StageApplied,
	hunt.StageInterview,
	hunt.StageOffer,
}

const starTestUser = "test_admin"

// TestStore_ToggleShortlistStar_StarOn verifies that toggling a job with no
// prior rating (or StageNew) creates a hunt_ratings row with StageSaved and
// returns starred=true.
//
// Red-on-revert: removing ToggleShortlistStar → compile error. Reverting the
// stage-to-saved logic → stage=="new" and assertion fails.
func TestStore_ToggleShortlistStar_StarOn(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	id := insertStarTestJob(t, s)

	// No prior rating — star ON.
	starred, err := s.ToggleShortlistStar(ctx, id, starTestUser, activeStages)
	if err != nil {
		t.Fatalf("ToggleShortlistStar (star on): %v", err)
	}
	if !starred {
		t.Errorf("ToggleShortlistStar (star on): want starred=true, got false")
	}

	// Verify DB: stage must be StageSaved.
	var stage string
	if err := pool.QueryRow(ctx,
		"SELECT stage FROM hunt_ratings WHERE entry_kind='job' AND entry_id=$1 AND user_name=$2",
		id, starTestUser,
	).Scan(&stage); err != nil {
		t.Fatalf("read stage after star on: %v", err)
	}
	if stage != hunt.StageSaved {
		t.Errorf("stage after star on: want %q, got %q", hunt.StageSaved, stage)
	}
}

// TestStore_ToggleShortlistStar_StarOff verifies that toggling a job whose
// stage is in activeStages demotes it to StageNew and returns starred=false.
//
// Red-on-revert: reverting the stage-to-new logic → stage remains "saved" and assertion fails.
func TestStore_ToggleShortlistStar_StarOff(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	id := insertStarTestJob(t, s)

	// Seed an active rating (StageSaved = starred).
	if err := s.Rate(ctx, "job", id, starTestUser, hunt.StageSaved, "keep this note"); err != nil {
		t.Fatalf("Rate (seed): %v", err)
	}

	// Star OFF.
	starred, err := s.ToggleShortlistStar(ctx, id, starTestUser, activeStages)
	if err != nil {
		t.Fatalf("ToggleShortlistStar (star off): %v", err)
	}
	if starred {
		t.Errorf("ToggleShortlistStar (star off): want starred=false, got true")
	}

	// Verify DB: stage must be StageNew.
	var stage string
	if err := pool.QueryRow(ctx,
		"SELECT stage FROM hunt_ratings WHERE entry_kind='job' AND entry_id=$1 AND user_name=$2",
		id, starTestUser,
	).Scan(&stage); err != nil {
		t.Fatalf("read stage after star off: %v", err)
	}
	if stage != hunt.StageNew {
		t.Errorf("stage after star off: want %q, got %q", hunt.StageNew, stage)
	}
}

// TestStore_ToggleShortlistStar_NotePreserved verifies that note text is never
// wiped when toggling star on or off.
//
// Red-on-revert: using SET note='' in the upsert ON CONFLICT clause → note="".
func TestStore_ToggleShortlistStar_NotePreserved(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	id := insertStarTestJob(t, s)
	const wantNote = "important note do not delete"

	// Seed a rating with a note.
	if err := s.Rate(ctx, "job", id, starTestUser, hunt.StageSaved, wantNote); err != nil {
		t.Fatalf("Rate (seed): %v", err)
	}

	// Star OFF — note must survive.
	if _, err := s.ToggleShortlistStar(ctx, id, starTestUser, activeStages); err != nil {
		t.Fatalf("ToggleShortlistStar (star off): %v", err)
	}
	var noteAfterOff *string
	if err := pool.QueryRow(ctx,
		"SELECT note FROM hunt_ratings WHERE entry_kind='job' AND entry_id=$1 AND user_name=$2",
		id, starTestUser,
	).Scan(&noteAfterOff); err != nil {
		t.Fatalf("read note after star off: %v", err)
	}
	if noteAfterOff == nil || *noteAfterOff != wantNote {
		t.Errorf("note after star off: want %q, got %v", wantNote, noteAfterOff)
	}

	// Star ON again — note must still survive.
	if _, err := s.ToggleShortlistStar(ctx, id, starTestUser, activeStages); err != nil {
		t.Fatalf("ToggleShortlistStar (star on again): %v", err)
	}
	var noteAfterOn *string
	if err := pool.QueryRow(ctx,
		"SELECT note FROM hunt_ratings WHERE entry_kind='job' AND entry_id=$1 AND user_name=$2",
		id, starTestUser,
	).Scan(&noteAfterOn); err != nil {
		t.Fatalf("read note after star on: %v", err)
	}
	if noteAfterOn == nil || *noteAfterOn != wantNote {
		t.Errorf("note after star on: want %q, got %v", wantNote, noteAfterOn)
	}
}

// TestStore_ToggleShortlistStar_AppliedStageIsStarred verifies that a job at
// StageApplied (an active stage) is treated as starred, and toggling it
// demotes to StageNew.
//
// Red-on-revert: removing StageApplied from activeStages → job appears unstarred,
// toggle goes the wrong direction.
func TestStore_ToggleShortlistStar_AppliedStageIsStarred(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	id := insertStarTestJob(t, s)

	// Seed at StageApplied (pipeline stage — definitely in shortlist).
	if err := s.Rate(ctx, "job", id, starTestUser, hunt.StageApplied, ""); err != nil {
		t.Fatalf("Rate (seed applied): %v", err)
	}

	// Toggle: applied is active → should turn star OFF (demote to new).
	starred, err := s.ToggleShortlistStar(ctx, id, starTestUser, activeStages)
	if err != nil {
		t.Fatalf("ToggleShortlistStar (applied): %v", err)
	}
	if starred {
		t.Errorf("ToggleShortlistStar on applied stage: want starred=false (demoted to new), got true")
	}

	var stage string
	if err := pool.QueryRow(ctx,
		"SELECT stage FROM hunt_ratings WHERE entry_kind='job' AND entry_id=$1 AND user_name=$2",
		id, starTestUser,
	).Scan(&stage); err != nil {
		t.Fatalf("read stage after toggle on applied: %v", err)
	}
	if stage != hunt.StageNew {
		t.Errorf("stage after toggle on applied: want %q (new), got %q", hunt.StageNew, stage)
	}
}

// TestStore_ToggleShortlistStar_StarStateReflectsActiveStages verifies that a
// job at StageDiscarded (not in activeStages) is treated as unstarred and
// toggling stars it (sets StageSaved).
//
// Red-on-revert: reverting the activeStages membership check → discarded job
// treated as starred, toggle goes wrong direction.
func TestStore_ToggleShortlistStar_StarStateReflectsActiveStages(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	s := hunt.NewStore(pool)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	id := insertStarTestJob(t, s)

	// Seed at StageDiscarded — NOT in activeStages.
	if err := s.Rate(ctx, "job", id, starTestUser, hunt.StageDiscarded, ""); err != nil {
		t.Fatalf("Rate (seed discarded): %v", err)
	}

	// Toggle: discarded is not active → should turn star ON (set saved).
	starred, err := s.ToggleShortlistStar(ctx, id, starTestUser, activeStages)
	if err != nil {
		t.Fatalf("ToggleShortlistStar (discarded): %v", err)
	}
	if !starred {
		t.Errorf("ToggleShortlistStar on discarded stage: want starred=true, got false")
	}

	var stage string
	if err := pool.QueryRow(ctx,
		"SELECT stage FROM hunt_ratings WHERE entry_kind='job' AND entry_id=$1 AND user_name=$2",
		id, starTestUser,
	).Scan(&stage); err != nil {
		t.Fatalf("read stage after toggle on discarded: %v", err)
	}
	if stage != hunt.StageSaved {
		t.Errorf("stage after toggle on discarded: want %q, got %q", hunt.StageSaved, stage)
	}
}
