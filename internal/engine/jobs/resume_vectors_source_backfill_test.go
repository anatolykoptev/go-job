package jobs

// resume_vectors_source_backfill_test.go — tests for the 007 backfill migration
// that relabels pre-existing derived rows (ref_id IS NOT NULL) from the
// schema-004 default source='agent' to source='profile'.
//
// Falsification:
//   - TestResumeVectors_SourceBackfill_PreservesManualRows goes RED if the
//     migration predicate is widened to include ref_id IS NULL (a manual row
//     gets relabeled to source='profile').
//   - TestResumeVectors_SourceBackfill_Idempotent goes RED if the migration is
//     not idempotent (a second run changes rows the first run already fixed).
//
// Runs against a real Postgres (gojob_test); skips when DATABASE_URL is unset.

import (
	"context"
	"os"
	"testing"
)

// runBackfillMigration reads the 007 migration file and executes it against the
// test DB, so the test exercises the real artifact rather than a hand-copied SQL
// string.
func runBackfillMigration(t *testing.T, db *ResumeDB) {
	t.Helper()
	sql, err := os.ReadFile("schema/007_resume_vectors_source_backfill.sql")
	if err != nil {
		t.Fatalf("read 007 migration: %v", err)
	}
	if _, err := db.pool.Exec(context.Background(), string(sql)); err != nil {
		t.Fatalf("exec 007 migration: %v", err)
	}
}

// TestResumeVectors_SourceBackfill_PreservesManualRows is the F3 regression: the
// backfill must relabel pre-existing derived rows (ref_id IS NOT NULL,
// source='agent' from the schema-004 default) to source='profile', while manual
// free-text memories (ref_id IS NULL, source='agent') are never touched.
//
// Mutant — widen the predicate to include ref_id IS NULL (e.g. drop the
// `ref_id IS NOT NULL` filter, or match `WHERE source='agent'`) → the manual row
// is relabeled to source='profile' → RED.
func TestResumeVectors_SourceBackfill_PreservesManualRows(t *testing.T) {
	db := testResumeDB(t)
	ctx := context.Background()

	// Manual free-text memory: ref_id=NULL, source='agent' (the schema default).
	manualID, err := db.UpsertVector(ctx, "manual memory that must stay agent", "note", nil)
	if err != nil {
		t.Fatalf("UpsertVector manual: %v", err)
	}

	// Pre-existing derived row written before the source discriminator existed:
	// ref_id NOT NULL, source='agent' (the schema-004 default it would carry).
	// Inserted via raw SQL because the API now mechanically rejects
	// source='agent' with a non-nil ref_id — exactly the bad pairing this row
	// represents (a historical artifact the backfill exists to fix).
	derivedRefID := int64(99999)
	derivedContent := "stale derived row from pre-branch build"
	derivedHash := vectorContentHash(resumeVectorUser, memTypeResumeExp, &derivedRefID, derivedContent)
	var derivedID int64
	if err := db.pool.QueryRow(ctx, `
		INSERT INTO resume_vectors (user_name, content, mem_type, source, ref_id, content_hash)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id
	`, resumeVectorUser, derivedContent, memTypeResumeExp, sourceAgent, derivedRefID, derivedHash).Scan(&derivedID); err != nil {
		t.Fatalf("seed stale derived row: %v", err)
	}

	runBackfillMigration(t, db)

	var manualSource string
	if err := db.pool.QueryRow(ctx,
		`SELECT source FROM resume_vectors WHERE id=$1`, manualID,
	).Scan(&manualSource); err != nil {
		t.Fatal(err)
	}
	if manualSource != sourceAgent {
		t.Fatalf("manual ref_id=NULL row relabeled by backfill: source=%q, want %q — "+
			"the migration must never touch ref_id IS NULL rows", manualSource, sourceAgent)
	}

	var derivedSource string
	if err := db.pool.QueryRow(ctx,
		`SELECT source FROM resume_vectors WHERE id=$1`, derivedID,
	).Scan(&derivedSource); err != nil {
		t.Fatal(err)
	}
	if derivedSource != sourceProfile {
		t.Fatalf("derived ref_id NOT NULL row not backfilled: source=%q, want %q — "+
			"pre-existing derived rows must be relabeled to source='profile'", derivedSource, sourceProfile)
	}
}

// TestResumeVectors_SourceBackfill_Idempotent proves running the backfill twice
// changes nothing the second time (the `source = 'agent'` predicate no longer
// matches rows the first run relabeled).
func TestResumeVectors_SourceBackfill_Idempotent(t *testing.T) {
	db := testResumeDB(t)
	ctx := context.Background()

	// Pre-existing derived row: source='agent' (schema-004 default), ref_id NOT
	// NULL. Inserted via raw SQL — the API now rejects source='agent' + non-nil
	// ref_id, so the historical state must be seeded directly.
	derivedRefID := int64(88888)
	derivedContent := "derived row for idempotency check"
	derivedHash := vectorContentHash(resumeVectorUser, memTypeResumeProj, &derivedRefID, derivedContent)
	var derivedID int64
	if err := db.pool.QueryRow(ctx, `
		INSERT INTO resume_vectors (user_name, content, mem_type, source, ref_id, content_hash)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id
	`, resumeVectorUser, derivedContent, memTypeResumeProj, sourceAgent, derivedRefID, derivedHash).Scan(&derivedID); err != nil {
		t.Fatalf("seed stale derived row: %v", err)
	}

	runBackfillMigration(t, db)

	var afterFirst string
	if err := db.pool.QueryRow(ctx,
		`SELECT source FROM resume_vectors WHERE id=$1`, derivedID,
	).Scan(&afterFirst); err != nil {
		t.Fatal(err)
	}
	if afterFirst != sourceProfile {
		t.Fatalf("first backfill did not relabel: source=%q, want %q", afterFirst, sourceProfile)
	}

	// Second run must be a no-op on already-relabeled rows.
	runBackfillMigration(t, db)

	var afterSecond string
	if err := db.pool.QueryRow(ctx,
		`SELECT source FROM resume_vectors WHERE id=$1`, derivedID,
	).Scan(&afterSecond); err != nil {
		t.Fatal(err)
	}
	if afterSecond != sourceProfile {
		t.Fatalf("second backfill changed an already-profile row: source=%q, want %q — "+
			"migration must be idempotent", afterSecond, sourceProfile)
	}
}
