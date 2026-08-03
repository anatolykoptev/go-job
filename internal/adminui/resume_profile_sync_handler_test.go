package adminui

// resume_profile_sync_handler_test.go — handler-level test proving the admin
// resume edit mutation paths actually invoke the profile vector sync.
//
// Falsification: remove the AfterSave hook from the experiences Writer →
// no source='profile' derived row appears after the Save → RED.
//
// Requires DATABASE_URL pointing at a *_test Postgres; skips otherwise.

import (
	"context"
	"os"
	"testing"

	"github.com/anatolykoptev/go-panel/tenant"
	"github.com/anatolykoptev/go_job/internal/dbtest"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/jackc/pgx/v5/pgxpool"
)

// openResumeSyncTestDB connects to DATABASE_URL (must be *_test), publishes the
// pool as the package-level ResumeDB (so the handler's jobs.GetResumeDB() and
// the sync see it), purges test rows, and inserts a fresh person. Returns the
// person id. Cleans up on test end.
func openResumeSyncTestDB(t *testing.T) (personID int) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	dbtest.RequireTestDB(t, dsn)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	ctx := context.Background()
	db, err := jobs.ConnectResumeDB(ctx, dsn)
	if err != nil {
		t.Fatalf("ConnectResumeDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Purge any rows left by a previous run.
	if _, err := db.Pool().Exec(ctx,
		`DELETE FROM resume_vectors WHERE user_name = $1`, jobs.ResumeVectorUser(),
	); err != nil {
		t.Fatalf("purge resume_vectors: %v", err)
	}

	pid, err := db.InsertPerson(ctx, jobs.PersonRecord{Name: "Handler Sync Test"})
	if err != nil {
		t.Fatalf("InsertPerson: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool().Exec(ctx, `DELETE FROM resume_vectors WHERE user_name = $1`, jobs.ResumeVectorUser())
		_, _ = db.Pool().Exec(ctx, `DELETE FROM resume_achievements WHERE person_id = $1`, pid)
		_, _ = db.Pool().Exec(ctx, `DELETE FROM resume_projects WHERE person_id = $1`, pid)
		_, _ = db.Pool().Exec(ctx, `DELETE FROM resume_experiences WHERE person_id = $1`, pid)
		_, _ = db.Pool().Exec(ctx, `DELETE FROM resume_persons WHERE id = $1`, pid)
	})

	jobs.SetResumeDB(db)
	t.Cleanup(func() { jobs.SetResumeDB(nil) })

	// No real embedder in CI — the sync must degrade to NULL embedding, not fail.
	prev := jobs.GetEmbedClient()
	jobs.SetEmbedClient(nil)
	t.Cleanup(func() { jobs.SetEmbedClient(prev) })

	return pid
}

// TestResumeExperienceWriterSave_SyncsVectors proves the experiences Writer's
// Save + AfterSave hook invokes the profile vector sync: after Save, a
// source='profile' derived row carrying the new experience id as ref_id must
// exist in resume_vectors.
//
// Mutant — remove the AfterSave hook from the Writer → no derived row → RED.
func TestResumeExperienceWriterSave_SyncsVectors(t *testing.T) {
	pid := openResumeSyncTestDB(t)

	// Build the experiences resource and extract its Writer Save + AfterSave.
	res := experiencesResource(nil) // pool not needed — Lister uses GetResumeDB
	if res.Writer == nil || res.Writer.Save == nil {
		t.Fatal("experiences resource Writer or Save is nil")
	}

	ctx := context.Background()
	values := map[string]string{
		"title":   "Principal Engineer",
		"company": "SyncCo",
	}
	if err := res.Writer.Save(ctx, tenant.Tenant{}, "", values); err != nil {
		t.Fatalf("Writer.Save failed: %v", err)
	}
	// Manually invoke AfterSave as the saveHandler would.
	if res.Writer.AfterSave != nil {
		res.Writer.AfterSave(ctx, "", nil)
	}

	dsn := os.Getenv("DATABASE_URL")
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New verify: %v", err)
	}
	defer pool.Close()

	var (
		source   string
		refID    *int64
		rowCount int
	)
	if err := pool.QueryRow(context.Background(),
		`SELECT source, ref_id FROM resume_vectors
		 WHERE user_name = $1 AND source = $2 AND mem_type = $3 AND ref_id IS NOT NULL
		 LIMIT 1`,
		jobs.ResumeVectorUser(), jobs.SourceProfile(), "resume_experience",
	).Scan(&source, &refID); err != nil {
		t.Fatalf("no source='profile' derived row found after Writer.Save + AfterSave — "+
			"the hook must invoke the profile vector sync (mutation→sync): %v", err)
	}
	if source != jobs.SourceProfile() {
		t.Errorf("derived row source = %q, want %q", source, jobs.SourceProfile())
	}
	if refID == nil {
		t.Error("derived row ref_id is NULL, want the new experience id")
	}

	_ = pool.QueryRow(context.Background(),
		`SELECT count(*) FROM resume_vectors WHERE user_name=$1 AND source=$2 AND mem_type=$3`,
		jobs.ResumeVectorUser(), jobs.SourceProfile(), "resume_experience",
	).Scan(&rowCount)
	if rowCount != 1 {
		t.Errorf("expected exactly 1 derived experience row, got %d (no duplicates)", rowCount)
	}

	// The person row inserted by the helper must match (sanity).
	if int(*refID) == 0 {
		t.Errorf("ref_id = %v, want a positive experience id", *refID)
	}
	_ = pid
}
