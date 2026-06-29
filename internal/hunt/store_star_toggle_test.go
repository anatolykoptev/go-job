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

// testPipelineStages are the pipeline stages the star toggle protects against
// accidental star-off. Mirrors adminui.shortlistPipelineValues.
var testPipelineStages = []string{
	hunt.StageClaimed,
	hunt.StageApplied,
	hunt.StageInterview,
	hunt.StageOffer,
}

const starTestUser = "test_admin"

// toggleStar is a test helper that calls ToggleShortlistStar with the standard
// pipeline-protection stages and soft-demotable triage values.
func toggleStar(t *testing.T, s *hunt.Store, id int64) bool {
	t.Helper()
	starred, err := s.ToggleShortlistStar(context.Background(), id, starTestUser,
		testPipelineStages, hunt.StarSoftTriageValues)
	if err != nil {
		t.Fatalf("ToggleShortlistStar: %v", err)
	}
	return starred
}

// readStage reads hunt_ratings.stage (pipeline axis) for a job.
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

// readTriage reads hunt_ratings.triage (triage axis) for a job.
// After migration 012 the star toggle operates exclusively on the triage column.
func readTriage(t *testing.T, s *hunt.Store, id int64) string {
	t.Helper()
	var triage string
	if err := s.Pool().QueryRow(context.Background(),
		"SELECT triage FROM hunt_ratings WHERE entry_kind='job' AND entry_id=$1 AND user_name=$2",
		id, starTestUser,
	).Scan(&triage); err != nil {
		t.Fatalf("readTriage: %v", err)
	}
	return triage
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
// no prior rating creates a row with triage='saved' and returns starred=true.
//
// After migration 012: star controls ONLY the triage column.
//
// Red-on-revert: removing ToggleShortlistStar → compile error.
// Reverting star-on path → triage=="" or starred=false.
func TestStore_ToggleShortlistStar_StarOn_NoRow(t *testing.T) {
	s, close := migratedStore(t)
	defer close()

	id := insertStarTestJob(t, s)

	starred := toggleStar(t, s, id)
	if !starred {
		t.Errorf("star on (no row): want starred=true, got false")
	}
	if got := readTriage(t, s, id); got != hunt.StageSaved {
		t.Errorf("triage after star on (no row): want %q, got %q", hunt.StageSaved, got)
	}
	// Star-on must NOT touch the pipeline stage column.
	if got := readStage(t, s, id); got != "" {
		t.Errorf("stage after star on (no row): must be empty (untouched), got %q", got)
	}
}

// TestStore_ToggleShortlistStar_StarOn_FromUnrated verifies that toggling a job
// with an empty-string triage (untriaged) stars it (sets triage='saved').
//
// Red-on-revert: reverting star-on path → triage stays "".
func TestStore_ToggleShortlistStar_StarOn_FromUnrated(t *testing.T) {
	s, close := migratedStore(t)
	defer close()

	id := insertStarTestJob(t, s)
	// Insert an explicit empty-triage row (representing a post-migration "new" job).
	if err := s.Rate(context.Background(), "job", id, starTestUser, "", "", ""); err != nil {
		t.Fatalf("Rate (seed untriaged): %v", err)
	}

	starred := toggleStar(t, s, id)
	if !starred {
		t.Errorf("star on from untriaged: want starred=true, got false")
	}
	if got := readTriage(t, s, id); got != hunt.StageSaved {
		t.Errorf("triage after star on from untriaged: want %q, got %q", hunt.StageSaved, got)
	}
}

// TestStore_ToggleShortlistStar_StarOn_NotePreserved verifies that star-on from
// an unrated-but-noted state preserves the existing note.
//
// The note column is excluded from the ON CONFLICT SET list in ToggleShortlistStar
// so that a star click never silently wipes an existing note. This test guards that
// invariant: seed a row with note but empty triage → toggle star → note survives.
//
// Red-on-revert: including note in ON CONFLICT SET clause → note wiped.
//
// Note: seeding from triage='discarded' is intentionally avoided here; discarded is
// a protected state (triage ∉ softDemotable) and star-on produces a NO-OP (starred=false).
// That contract is tested separately in TestStore_ToggleShortlistStar_Discarded_IsNoOp.
func TestStore_ToggleShortlistStar_StarOn_NotePreserved(t *testing.T) {
	s, close := migratedStore(t)
	defer close()

	id := insertStarTestJob(t, s)
	const wantNote = "do not delete this note"

	// Seed: empty triage + existing note. Rate with triage="" uses CASE guard
	// (preserves existing triage=''), so this is "unrated with a note".
	if err := s.Rate(context.Background(), "job", id, starTestUser, "", "", wantNote); err != nil {
		t.Fatalf("Rate (seed unrated with note): %v", err)
	}

	starred := toggleStar(t, s, id)
	if !starred {
		t.Errorf("star on from unrated: want starred=true, got false")
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

// TestStore_ToggleShortlistStar_SoftDemote tests star-off from each of the two
// soft (demotable) triage values → should clear triage to '', return starred=false.
//
// After migration 012: star-off clears the triage column (NOT stage). The pipeline
// stage is never touched. The legacy 'claimed' value is now a pipeline stage and is
// handled by TestStore_ToggleShortlistStar_AdvancedStageNoOp.
//
// Red-on-revert: removing the soft-stage branch → triage unchanged, starred=true.
func TestStore_ToggleShortlistStar_SoftDemote(t *testing.T) {
	// Only triage-axis values are soft-demotable.
	softCases := []string{hunt.StageInteresting, hunt.StageSaved}
	for _, initialTriage := range softCases {
		initialTriage := initialTriage
		t.Run(initialTriage, func(t *testing.T) {
			s, close := migratedStore(t)
			defer close()

			id := insertStarTestJob(t, s)
			// Seed via triage axis.
			if err := s.Rate(context.Background(), "job", id, starTestUser, initialTriage, "", "my note"); err != nil {
				t.Fatalf("Rate (seed triage=%s): %v", initialTriage, err)
			}

			starred := toggleStar(t, s, id)
			if starred {
				t.Errorf("star off from triage=%s: want starred=false, got true", initialTriage)
			}
			// Triage must be cleared.
			if got := readTriage(t, s, id); got != "" {
				t.Errorf("triage after star off from %s: want \"\", got %q", initialTriage, got)
			}
			// Pipeline stage must be untouched.
			if got := readStage(t, s, id); got != "" {
				t.Errorf("stage after star off from triage=%s: must be untouched (\"\"), got %q", initialTriage, got)
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

	if err := s.Rate(context.Background(), "job", id, starTestUser, hunt.StageSaved, "", wantNote); err != nil {
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
// job at any pipeline stage (claimed/applied/interview/offer) is a NO-OP:
// stage must be unchanged and starred=true must be returned.
//
// After migration 012: claimed is now a pipeline stage (moved from triage-soft to
// pipeline-protected), alongside applied/interview/offer.
//
// This is the key operator-safety requirement: a star click can NEVER lose a
// pipeline stage.
//
// Red-on-revert: removing the advanced-stage guard → claimed/applied/etc. clears
// triage; starred=false assertion fails.
func TestStore_ToggleShortlistStar_AdvancedStageNoOp(t *testing.T) {
	// All pipeline stages must be protected — including claimed (moved post-012).
	pipelineCases := []string{
		hunt.StageClaimed, hunt.StageApplied, hunt.StageInterview, hunt.StageOffer,
	}
	for _, initialStage := range pipelineCases {
		initialStage := initialStage
		t.Run(initialStage, func(t *testing.T) {
			s, close := migratedStore(t)
			defer close()

			id := insertStarTestJob(t, s)
			// Seed via pipeline axis (triage="", stage=initialStage).
			if err := s.Rate(context.Background(), "job", id, starTestUser, "", initialStage, "pipeline note"); err != nil {
				t.Fatalf("Rate (seed stage=%s): %v", initialStage, err)
			}

			starred := toggleStar(t, s, id)
			if !starred {
				t.Errorf("pipeline stage %s no-op: want starred=true (unchanged), got false", initialStage)
			}
			if got := readStage(t, s, id); got != initialStage {
				t.Errorf("pipeline stage %s no-op: stage must be UNCHANGED, got %q", initialStage, got)
			}
		})
	}
}

// TestStore_ToggleShortlistStar_Discarded_IsNoOp verifies that a star click on a
// job with triage='discarded' is a deliberate-negative-decision NO-OP: the triage
// column is NOT overwritten with 'saved', and starred=false is returned.
//
// Design contract (types.go: StarSoftTriageValues excludes discarded):
// a negative triage decision is explicit — a star click can never silently clear it.
//
// Red-on-revert: removing the triage-protection guard in ToggleShortlistStar →
// triage is overwritten with 'saved'; this test fails on both assertions.
func TestStore_ToggleShortlistStar_Discarded_IsNoOp(t *testing.T) {
	s, close := migratedStore(t)
	defer close()

	id := insertStarTestJob(t, s)
	if err := s.Rate(context.Background(), "job", id, starTestUser, hunt.StageDiscarded, "", ""); err != nil {
		t.Fatalf("Rate (seed discarded): %v", err)
	}

	starred := toggleStar(t, s, id)
	if starred {
		t.Errorf("star click on discarded: want starred=false (no-op), got true")
	}
	if got := readTriage(t, s, id); got != hunt.StageDiscarded {
		t.Errorf("triage after star click on discarded: want %q (unchanged), got %q", hunt.StageDiscarded, got)
	}
}

// TestStore_ToggleShortlistStar_StarSoftTriageValues_Aligned verifies that
// hunt.StarSoftTriageValues covers exactly {interesting, saved} — the set that
// toggleStar demotes. If new triage values are added without updating this set,
// star behaviour breaks silently.
//
// Red-on-revert: adding a triage value to StarSoftTriageValues without intent →
// star-off behaviour widens unexpectedly.
func TestStore_ToggleShortlistStar_StarSoftTriageValues_Aligned(t *testing.T) {
	expected := map[string]bool{
		hunt.StageInteresting: true,
		hunt.StageSaved:       true,
	}
	if len(hunt.StarSoftTriageValues) != len(expected) {
		t.Errorf("StarSoftTriageValues: want %d entries, got %d: %v",
			len(expected), len(hunt.StarSoftTriageValues), hunt.StarSoftTriageValues)
	}
	for _, v := range hunt.StarSoftTriageValues {
		if !expected[v] {
			t.Errorf("StarSoftTriageValues: unexpected value %q", v)
		}
	}
}
