package jobs

// resume_profile_sync_test.go — tests for SyncProfileVectors (derived vector
// rows re-derived from the structured profile entities).
//
// Falsification:
//   - TestResumeExperienceCreateHandler_SyncsVectors (adminui) goes RED when the
//     sync call is removed from the mutation path (no derived vector row).
//   - TestSyncProfileVectors_ManualAgentRowsUntouched goes RED if the sync is
//     made to overwrite/delete source='agent' rows (e.g. orphan-delete loses the
//     source='profile' scope).
//
// All tests run against a real Postgres (gojob_test); they skip when
// DATABASE_URL is unset.

import (
	"context"
	"testing"
)

// testResumeDBWithPerson returns a ResumeDB with all test rows (source='agent'
// AND source='profile') purged and a fresh person inserted, so each test starts
// clean. The person is the profile the sync re-derives from. The DB is also
// published as the package-level resumeDB (SetResumeDB) so SyncProfileVectors —
// which reads GetResumeDB() — sees it.
func testResumeDBWithPerson(t *testing.T) (*ResumeDB, int) {
	t.Helper()
	db := testResumeDB(t)
	SetResumeDB(db)
	t.Cleanup(func() { SetResumeDB(nil) })
	ctx := context.Background()

	// testResumeDB only purges source='agent'; purge derived rows too so tests
	// are idempotent across runs.
	if _, err := db.pool.Exec(ctx,
		`DELETE FROM resume_vectors WHERE user_name = $1 AND source = 'profile'`,
		resumeVectorUser,
	); err != nil {
		t.Fatalf("cleanup derived resume_vectors: %v", err)
	}

	pid, err := db.InsertPerson(ctx, PersonRecord{Name: "Sync Test Person"})
	if err != nil {
		t.Fatalf("InsertPerson: %v", err)
	}
	t.Cleanup(func() {
		// Remove the person's entities + vectors so the next test is clean.
		_, _ = db.pool.Exec(ctx, `DELETE FROM resume_vectors WHERE user_name = $1`, resumeVectorUser)
		_, _ = db.pool.Exec(ctx, `DELETE FROM resume_achievements WHERE person_id = $1`, pid)
		_, _ = db.pool.Exec(ctx, `DELETE FROM resume_projects WHERE person_id = $1`, pid)
		_, _ = db.pool.Exec(ctx, `DELETE FROM resume_experiences WHERE person_id = $1`, pid)
		_, _ = db.pool.Exec(ctx, `DELETE FROM resume_persons WHERE id = $1`, pid)
	})
	return db, pid
}

// TestSyncProfileVectors_CreatesDerivedRows proves the sync creates
// source='profile' derived rows carrying the entity id as ref_id and the
// re-derived content, reusing UpsertVectorWithSource + content-hash dedup.
func TestSyncProfileVectors_CreatesDerivedRows(t *testing.T) {
	db, pid := testResumeDBWithPerson(t)
	ctx := context.Background()

	expID, err := db.InsertExperience(ctx, pid, ExperienceRecord{
		Title:       "Staff Engineer",
		Company:     "Acme",
		StartDate:   "2020-01",
		EndDate:     "2023-12",
		Description: "Led platform team",
		Highlights:  []string{"cut p99 by 40%"},
	})
	if err != nil {
		t.Fatalf("InsertExperience: %v", err)
	}

	if err := SyncProfileVectors(ctx, pid); err != nil {
		t.Fatalf("SyncProfileVectors: %v", err)
	}

	var (
		source   string
		refID    *int64
		content  string
		memType  string
		rowCount int
	)
	err = db.pool.QueryRow(ctx,
		`SELECT source, ref_id, content, mem_type FROM resume_vectors
		 WHERE user_name=$1 AND mem_type=$2 AND ref_id=$3`,
		resumeVectorUser, memTypeResumeExp, expID,
	).Scan(&source, &refID, &content, &memType)
	if err != nil {
		t.Fatalf("derived row not found: %v", err)
	}
	if source != sourceProfile {
		t.Errorf("source = %q, want %q (derived rows must be distinguishable from manual agent rows)", source, sourceProfile)
	}
	if refID == nil || int(*refID) != expID {
		t.Errorf("ref_id = %v, want %d (derived row must carry the entity id)", refID, expID)
	}
	wantContent := formatExperienceTextExtended("Staff Engineer", "Acme", "2020-01", "2023-12", "Led platform team", []string{"cut p99 by 40%"}, "")
	if content != wantContent {
		t.Errorf("content = %q, want %q", content, wantContent)
	}

	_ = db.pool.QueryRow(ctx,
		`SELECT count(*) FROM resume_vectors WHERE user_name=$1 AND source=$2 AND mem_type=$3`,
		resumeVectorUser, sourceProfile, memTypeResumeExp,
	).Scan(&rowCount)
	if rowCount != 1 {
		t.Errorf("expected 1 derived experience row, got %d (no duplicates)", rowCount)
	}
}

// TestSyncProfileVectors_ManualAgentRowsUntouched is the regression the task
// cares most about: manual source='agent' rows must never be touched by the
// sync. The manual row here shares mem_type='resume_experience' with a derived
// row but has source='agent' and ref_id=NULL — the most likely accidental
// victim of an under-scoped orphan delete. After the sync runs (which creates a
// derived row for a real experience and deletes orphans), the manual row must
// survive with its content and source intact.
//
// Mutant — drop the source='profile' scope from DeleteDerivedVectorsNotIn (or
// from ListDerivedVectors) → the manual source='agent' row is deleted/overwritten
// → RED.
func TestSyncProfileVectors_ManualAgentRowsUntouched(t *testing.T) {
	db, pid := testResumeDBWithPerson(t)
	ctx := context.Background()

	// Manual memory sharing a derived mem_type but source='agent', ref_id=NULL.
	const manualContent = "manual agent memory that must survive sync"
	manualID, err := db.UpsertVector(ctx, manualContent, memTypeResumeExp, nil, nil)
	if err != nil {
		t.Fatalf("UpsertVector manual: %v", err)
	}

	// A real experience → the sync will create a derived row AND run orphan delete.
	expID, err := db.InsertExperience(ctx, pid, ExperienceRecord{
		Title: "Engineer", Company: "Beta",
	})
	if err != nil {
		t.Fatalf("InsertExperience: %v", err)
	}
	if err := SyncProfileVectors(ctx, pid); err != nil {
		t.Fatalf("SyncProfileVectors: %v", err)
	}

	var exists int
	if err := db.pool.QueryRow(ctx,
		`SELECT count(*) FROM resume_vectors WHERE id=$1`,
		manualID,
	).Scan(&exists); err != nil {
		t.Fatalf("query manual row: %v", err)
	}
	if exists != 1 {
		t.Fatalf("manual source='agent' row was deleted by the sync (exists=%d) — "+
			"the sync must never touch source='agent' rows", exists)
	}
	var (
		source  string
		content string
		refID   *int64
	)
	if err := db.pool.QueryRow(ctx,
		`SELECT source, content, ref_id FROM resume_vectors WHERE id=$1`,
		manualID,
	).Scan(&source, &content, &refID); err != nil {
		t.Fatal(err)
	}
	if source != sourceAgent {
		t.Errorf("manual row source overwritten: got %q, want %q", source, sourceAgent)
	}
	if content != manualContent {
		t.Errorf("manual row content overwritten: got %q, want %q", content, manualContent)
	}
	if refID != nil {
		t.Errorf("manual row ref_id overwritten: got %v, want NULL", refID)
	}

	// And the derived row for the real experience must still exist (sync worked).
	var derivedExists int
	_ = db.pool.QueryRow(ctx,
		`SELECT count(*) FROM resume_vectors WHERE user_name=$1 AND source=$2 AND mem_type=$3 AND ref_id=$4`,
		resumeVectorUser, sourceProfile, memTypeResumeExp, expID,
	).Scan(&derivedExists)
	if derivedExists != 1 {
		t.Errorf("derived row for experience %d missing (got %d)", expID, derivedExists)
	}
}

// TestSyncProfileVectors_EmbedFailureDegrades proves that with the embedder
// unreachable (nil), a profile sync still persists the derived row with a NULL
// embedding for a later backfill, and returns no error.
func TestSyncProfileVectors_EmbedFailureDegrades(t *testing.T) {
	db, pid := testResumeDBWithPerson(t)
	prev := GetEmbedClient()
	SetEmbedClient(nil) // embedder unreachable
	t.Cleanup(func() { SetEmbedClient(prev) })

	ctx := context.Background()
	expID, err := db.InsertExperience(ctx, pid, ExperienceRecord{
		Title: "Engineer", Company: "Gamma",
	})
	if err != nil {
		t.Fatalf("InsertExperience: %v", err)
	}

	if err := SyncProfileVectors(ctx, pid); err != nil {
		t.Fatalf("SyncProfileVectors returned error on embedder outage (must degrade, not abort): %v", err)
	}

	var (
		exists        int
		embeddingNull bool
	)
	err = db.pool.QueryRow(ctx,
		`SELECT count(*), true FROM resume_vectors WHERE user_name=$1 AND source=$2 AND mem_type=$3 AND ref_id=$4`,
		resumeVectorUser, sourceProfile, memTypeResumeExp, expID,
	).Scan(&exists, &embeddingNull)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if exists != 1 {
		t.Fatalf("derived row not persisted on embedder outage (exists=%d) — must degrade, not skip", exists)
	}
	err = db.pool.QueryRow(ctx,
		`SELECT embedding IS NULL FROM resume_vectors WHERE user_name=$1 AND source=$2 AND mem_type=$3 AND ref_id=$4`,
		resumeVectorUser, sourceProfile, memTypeResumeExp, expID,
	).Scan(&embeddingNull)
	if err != nil {
		t.Fatal(err)
	}
	if !embeddingNull {
		t.Error("expected embedding IS NULL on embedder outage (degrade for later backfill), got non-NULL")
	}
}

// TestSyncProfileVectors_NoOpOnUnchanged proves re-running the sync with
// unchanged data is a no-op: no duplicate rows and no updated_at churn.
func TestSyncProfileVectors_NoOpOnUnchanged(t *testing.T) {
	db, pid := testResumeDBWithPerson(t)
	ctx := context.Background()

	if _, err := db.InsertExperience(ctx, pid, ExperienceRecord{
		Title: "Engineer", Company: "Delta",
	}); err != nil {
		t.Fatalf("InsertExperience: %v", err)
	}
	if err := SyncProfileVectors(ctx, pid); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	var updatedAtFirst string
	if err := db.pool.QueryRow(ctx,
		`SELECT updated_at::text FROM resume_vectors WHERE user_name=$1 AND source=$2 AND mem_type=$3`,
		resumeVectorUser, sourceProfile, memTypeResumeExp,
	).Scan(&updatedAtFirst); err != nil {
		t.Fatalf("query updated_at after first sync: %v", err)
	}

	if err := SyncProfileVectors(ctx, pid); err != nil {
		t.Fatalf("second sync: %v", err)
	}

	var (
		updatedAtSecond string
		rowCount        int
	)
	if err := db.pool.QueryRow(ctx,
		`SELECT updated_at::text FROM resume_vectors WHERE user_name=$1 AND source=$2 AND mem_type=$3`,
		resumeVectorUser, sourceProfile, memTypeResumeExp,
	).Scan(&updatedAtSecond); err != nil {
		t.Fatalf("query updated_at after second sync: %v", err)
	}
	if err := db.pool.QueryRow(ctx,
		`SELECT count(*) FROM resume_vectors WHERE user_name=$1 AND source=$2 AND mem_type=$3`,
		resumeVectorUser, sourceProfile, memTypeResumeExp,
	).Scan(&rowCount); err != nil {
		t.Fatalf("query count after second sync: %v", err)
	}
	if rowCount != 1 {
		t.Errorf("expected 1 row after re-sync (no duplicates), got %d", rowCount)
	}
	if updatedAtFirst != updatedAtSecond {
		t.Errorf("updated_at churned on unchanged content: first=%s second=%s — re-sync must be a no-op",
			updatedAtFirst, updatedAtSecond)
	}
}

// TestSyncProfileVectors_RemovesOrphansOnDelete proves that deleting an entity
// and re-syncing removes its derived row (created on insert, removed on delete).
func TestSyncProfileVectors_RemovesOrphansOnDelete(t *testing.T) {
	db, pid := testResumeDBWithPerson(t)
	ctx := context.Background()

	expID, err := db.InsertExperience(ctx, pid, ExperienceRecord{
		Title: "Engineer", Company: "Epsilon",
	})
	if err != nil {
		t.Fatalf("InsertExperience: %v", err)
	}
	if err := SyncProfileVectors(ctx, pid); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	if err := db.DeleteExperience(ctx, expID); err != nil {
		t.Fatalf("DeleteExperience: %v", err)
	}
	if err := SyncProfileVectors(ctx, pid); err != nil {
		t.Fatalf("second sync: %v", err)
	}

	var rowCount int
	if err := db.pool.QueryRow(ctx,
		`SELECT count(*) FROM resume_vectors WHERE user_name=$1 AND source=$2 AND mem_type=$3 AND ref_id=$4`,
		resumeVectorUser, sourceProfile, memTypeResumeExp, expID,
	).Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	if rowCount != 0 {
		t.Errorf("orphaned derived row for deleted experience %d still present (got %d) — "+
			"sync must remove derived rows when the entity is deleted", expID, rowCount)
	}
}

// TestSyncProfileVectors_ExperienceDomainMatchesMasterResume is the F2
// regression: master_resume builds the experience vector with the parsed domain
// (formatExperienceTextExtended ... exp.Domain), but the sync used to build it
// with an EMPTY domain because GetAllExperiences never SELECTed the domain
// column. Different content → different content_hash → ON CONFLICT
// (user_name, content_hash) missed → a second resume_experience row for the
// same ref_id. Orphan-delete kept both (both ref_ids in keepIDs), so search
// returned duplicates and the no-op-on-unchanged invariant never converged.
//
// The test simulates the master_resume write (a source='profile' row with the
// domain-tagged content) for an experience whose domain is non-empty, then runs
// the sync and asserts a single row (no duplicate ref_id). With an empty domain
// the bug is invisible, so the experience is seeded with a non-empty domain.
//
// Mutant — revert buildDerivedEntries to pass "" (or revert GetAllExperiences to
// not SELECT domain) → the sync writes a second row with a different
// content_hash → rowCount=2 → RED.
func TestSyncProfileVectors_ExperienceDomainMatchesMasterResume(t *testing.T) {
	db, pid := testResumeDBWithPerson(t)
	ctx := context.Background()

	expID, err := db.InsertExperience(ctx, pid, ExperienceRecord{
		Title:       "Staff Engineer",
		Company:     "Acme",
		StartDate:   "2020-01",
		EndDate:     "2023-12",
		Description: "Led platform team",
		Highlights:  []string{"cut p99 by 40%"},
	})
	if err != nil {
		t.Fatalf("InsertExperience: %v", err)
	}
	// Set a non-empty domain — the condition under which the bug is visible.
	const domain = "Platform Engineering"
	if err := db.UpdateExperienceMeta(ctx, expID, nil, nil, domain, false); err != nil {
		t.Fatalf("UpdateExperienceMeta: %v", err)
	}

	// Simulate the master_resume write: a source='profile' row with the
	// domain-tagged content (exactly what BuildMasterResume produces).
	masterContent := formatExperienceTextExtended(
		"Staff Engineer", "Acme", "2020-01", "2023-12",
		"Led platform team", []string{"cut p99 by 40%"}, domain)
	expIDi64 := int64(expID)
	if _, err := db.UpsertVectorWithSource(ctx, masterContent, memTypeResumeExp, &expIDi64, nil, sourceProfile); err != nil {
		t.Fatalf("seed master_resume row: %v", err)
	}

	// Re-derive via the sync. With the fix, the re-derived content is
	// byte-identical (same domain) → ON CONFLICT updates the existing row →
	// still 1 row. Without the fix (empty domain), content_hash differs → a
	// second row is inserted.
	if err := SyncProfileVectors(ctx, pid); err != nil {
		t.Fatalf("SyncProfileVectors: %v", err)
	}

	var rowCount int
	if err := db.pool.QueryRow(ctx,
		`SELECT count(*) FROM resume_vectors WHERE user_name=$1 AND source=$2 AND mem_type=$3 AND ref_id=$4`,
		resumeVectorUser, sourceProfile, memTypeResumeExp, expID,
	).Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	if rowCount != 1 {
		t.Fatalf("expected 1 derived experience row (no duplicate ref_id), got %d — "+
			"sync content must match master_resume content (domain included)", rowCount)
	}

	// The surviving row must carry the domain-tagged content, not the
	// empty-domain variant.
	var content string
	if err := db.pool.QueryRow(ctx,
		`SELECT content FROM resume_vectors WHERE user_name=$1 AND source=$2 AND mem_type=$3 AND ref_id=$4`,
		resumeVectorUser, sourceProfile, memTypeResumeExp, expID,
	).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if content != masterContent {
		t.Errorf("derived content mismatch:\n got %q\nwant %q", content, masterContent)
	}
}
