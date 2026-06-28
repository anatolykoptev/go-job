package hunt_test

import (
	"context"
	"testing"

	"github.com/anatolykoptev/go_job/internal/hunt"
)

// insertStarTestJob inserts a bare-minimum hunt_jobs row for star toggle tests.
// Uses a unique URL per test name to avoid dedup-hash collisions across subtests.
func insertStarTestJob(t *testing.T, s *hunt.Store) int64 {
	t.Helper()
	j := hunt.Job{
		DedupHash: hunt.DedupHash("https://star-test.example/job/" + t.Name()),
		Title:     "Star Test Role",
		Company:   "StarCo",
		URL:       "https://star-test.example/job/" + t.Name(),
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

// testActiveStages mirrors adminui.shortlistActiveStages.
var testActiveStages = []string{
	hunt.StageInteresting,
	hunt.StageSaved,
	hunt.StageClaimed,
	hunt.StageApplied,
	hunt.StageInterview,
	hunt.StageOffer,
}

// testSoftStages mirrors hunt.StarSoftStages (the demotable set).
var testSoftStages = hunt.StarSoftStages

const starTestUser = "test_admin"

// toggleStar is a test helper that calls ToggleShortlistStar with the standard
// testActiveStages + testSoftStages used across all star tests.
func toggleStar(t *testing.T, s *hunt.Store, id int64) bool {
	t.Helper()
	starred, err := s.ToggleShortlistStar(context.Background(), id, starTestUser, testActiveStages, testSoftStages)
	if err != nil {
		t.Fatalf("ToggleShortlistStar: %v", err)
	}
	return starred
}

// readStage reads the hunt_ratings.stage for a job.
func readStage(t *testing.T, s *hunt.Store, id int64) string {
	t.Helper()
	var stage string
	if err := s.Pool().QueryRow(context.Background(),
		"SELECT stage FROM hunt_ratings WHERE entry_kind='job' AND entry_id=$1 AND user_name=$2",
		id, starTestUser,
	).Scan(&stage); err != nil {
		t.Fatalf("readStage: %v", err)
	}
	return stage
}

func migratedStore(t *testing.T) (*hunt.Store, func()) {
	t.Helper()
	pool := openTestPool(t)
	s := hunt.NewStore(pool)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s, pool.Close
}

// TestStore_ToggleShortlistStar_StarOn_NoRow verifies that toggling a job with
// no prior rating creates a StageSaved row and returns starred=true.
//
// Red-on-revert: removing ToggleShortlistStar → compile error.
// Reverting star-on path → stage=="new" or starred=false.
func TestStore_ToggleShortlistStar_StarOn_NoRow(t *testing.T) {
	s, close := migratedStore(t)
	defer close()

	id := insertStarTestJob(t, s)

	starred := toggleStar(t, s, id)
	if !starred {
		t.Errorf("star on (no row): want starred=true, got false")
	}
	if got := readStage(t, s, id); got != hunt.StageSaved {
		t.Errorf("stage after star on (no row): want %q, got %q", hunt.StageSaved, got)
	}
}

// TestStore_ToggleShortlistStar_StarOn_FromNew verifies that toggling a job at
// StageNew (not in activeStages) stars it (sets StageSaved).
//
// Red-on-revert: reverting star-on path → stage stays "new".
func TestStore_ToggleShortlistStar_StarOn_FromNew(t *testing.T) {
	s, close := migratedStore(t)
	defer close()

	id := insertStarTestJob(t, s)
	if err := s.Rate(context.Background(), "job", id, starTestUser, hunt.StageNew, ""); err != nil {
		t.Fatalf("Rate (seed new): %v", err)
	}

	starred := toggleStar(t, s, id)
	if !starred {
		t.Errorf("star on from new: want starred=true, got false")
	}
	if got := readStage(t, s, id); got != hunt.StageSaved {
		t.Errorf("stage after star on from new: want %q, got %q", hunt.StageSaved, got)
	}
}

// TestStore_ToggleShortlistStar_StarOn_NotePreserved verifies that star-on from
// StageDiscarded (not in activeStages) preserves an existing note.
//
// Red-on-revert: including note in ON CONFLICT SET clause → note wiped.
func TestStore_ToggleShortlistStar_StarOn_NotePreserved(t *testing.T) {
	s, close := migratedStore(t)
	defer close()

	id := insertStarTestJob(t, s)
	const wantNote = "do not delete this note"

	if err := s.Rate(context.Background(), "job", id, starTestUser, hunt.StageDiscarded, wantNote); err != nil {
		t.Fatalf("Rate (seed discarded with note): %v", err)
	}

	starred := toggleStar(t, s, id)
	if !starred {
		t.Errorf("star on from discarded: want starred=true, got false")
	}

	var note *string
	if err := s.Pool().QueryRow(context.Background(),
		"SELECT note FROM hunt_ratings WHERE entry_kind='job' AND entry_id=$1 AND user_name=$2",
		id, starTestUser,
	).Scan(&note); err != nil {
		t.Fatalf("read note: %v", err)
	}
	if note == nil || *note != wantNote {
		t.Errorf("note after star on: want %q, got %v", wantNote, note)
	}
}

// TestStore_ToggleShortlistStar_SoftDemote tests star-off from each of the
// three soft (demotable) stages → should demote to StageNew, return starred=false.
//
// Red-on-revert: removing the soft-stage branch → stage unchanged, starred=true.
func TestStore_ToggleShortlistStar_SoftDemote(t *testing.T) {
	softCases := []string{hunt.StageInteresting, hunt.StageSaved, hunt.StageClaimed}
	for _, initialStage := range softCases {
		initialStage := initialStage
		t.Run(initialStage, func(t *testing.T) {
			s, close := migratedStore(t)
			defer close()

			id := insertStarTestJob(t, s)
			if err := s.Rate(context.Background(), "job", id, starTestUser, initialStage, "my note"); err != nil {
				t.Fatalf("Rate (seed %s): %v", initialStage, err)
			}

			starred := toggleStar(t, s, id)
			if starred {
				t.Errorf("star off from %s: want starred=false, got true", initialStage)
			}
			if got := readStage(t, s, id); got != hunt.StageNew {
				t.Errorf("stage after star off from %s: want %q, got %q", initialStage, hunt.StageNew, got)
			}
		})
	}
}

// TestStore_ToggleShortlistStar_SoftDemote_NotePreserved verifies that note
// survives a soft-stage star-off.
//
// Red-on-revert: including note in SET clause → note wiped on demotion.
func TestStore_ToggleShortlistStar_SoftDemote_NotePreserved(t *testing.T) {
	s, close := migratedStore(t)
	defer close()

	id := insertStarTestJob(t, s)
	const wantNote = "important note keep on demotion"

	if err := s.Rate(context.Background(), "job", id, starTestUser, hunt.StageSaved, wantNote); err != nil {
		t.Fatalf("Rate (seed): %v", err)
	}

	starred := toggleStar(t, s, id)
	if starred {
		t.Errorf("star off from saved: want starred=false, got true")
	}

	var note *string
	if err := s.Pool().QueryRow(context.Background(),
		"SELECT note FROM hunt_ratings WHERE entry_kind='job' AND entry_id=$1 AND user_name=$2",
		id, starTestUser,
	).Scan(&note); err != nil {
		t.Fatalf("read note: %v", err)
	}
	if note == nil || *note != wantNote {
		t.Errorf("note after soft demotion: want %q, got %v", wantNote, note)
	}
}

// TestStore_ToggleShortlistStar_AdvancedStageNoOp tests that a star click on a
// job at an advanced pipeline stage (applied/interview/offer) is a NO-OP:
// stage must be unchanged and starred=true must be returned.
//
// This is the key operator-safety requirement: a star click can NEVER lose a
// pipeline stage.
//
// Red-on-revert: removing the advanced-stage guard → applied demotes to "new",
// starred=false → assertion fails on both stage and starred.
func TestStore_ToggleShortlistStar_AdvancedStageNoOp(t *testing.T) {
	advancedCases := []string{hunt.StageApplied, hunt.StageInterview, hunt.StageOffer}
	for _, initialStage := range advancedCases {
		initialStage := initialStage
		t.Run(initialStage, func(t *testing.T) {
			s, close := migratedStore(t)
			defer close()

			id := insertStarTestJob(t, s)
			if err := s.Rate(context.Background(), "job", id, starTestUser, initialStage, "pipeline note"); err != nil {
				t.Fatalf("Rate (seed %s): %v", initialStage, err)
			}

			starred := toggleStar(t, s, id)
			if !starred {
				t.Errorf("advanced stage %s no-op: want starred=true (unchanged), got false", initialStage)
			}
			if got := readStage(t, s, id); got != initialStage {
				t.Errorf("advanced stage %s no-op: stage must be UNCHANGED, got %q", initialStage, got)
			}
		})
	}
}

// TestStore_ToggleShortlistStar_StarStateReflectsActiveStages verifies that a
// job at StageDiscarded (not in activeStages) is treated as unstarred and
// toggling stars it (sets StageSaved).
//
// Red-on-revert: reverting the activeStages membership check → discarded job
// treated as starred, toggle goes wrong direction.
func TestStore_ToggleShortlistStar_StarStateReflectsActiveStages(t *testing.T) {
	s, close := migratedStore(t)
	defer close()

	id := insertStarTestJob(t, s)

	if err := s.Rate(context.Background(), "job", id, starTestUser, hunt.StageDiscarded, ""); err != nil {
		t.Fatalf("Rate (seed discarded): %v", err)
	}

	starred := toggleStar(t, s, id)
	if !starred {
		t.Errorf("star on from discarded: want starred=true, got false")
	}
	if got := readStage(t, s, id); got != hunt.StageSaved {
		t.Errorf("stage after toggle on discarded: want %q, got %q", hunt.StageSaved, got)
	}
}
