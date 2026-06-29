package jobs

import (
	"context"
	"os"
	"testing"

	"github.com/anatolykoptev/go_job/internal/dbtest"
)

// TestGetAllProjects_NullDescriptionAndURL is the regression test for the
// resume_profile "0 projects" read bug.
//
// Root cause: GetAllProjects scanned the nullable text columns description and
// url into non-pointer Go `string` fields. A single row with SQL NULL in either
// column made pgx fail the whole query scan, so GetAllProjects returned an error
// for the ENTIRE result set, and loadProjects (which swallowed the error) turned
// that into "0 projects". Real prod data had 9 of 16 project rows with NULL
// description.
//
// Falsification: revert the COALESCE() in GetAllProjects' SELECT and this test
// goes red — the scan errors on the NULL row and the project vanishes.
func TestGetAllProjects_NullDescriptionAndURL(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	dbtest.RequireTestDB(t, dsn)

	ctx := context.Background()
	db, err := ConnectResumeDB(ctx, dsn)
	if err != nil {
		t.Fatalf("ConnectResumeDB: %v", err)
	}
	t.Cleanup(db.Close)

	personID, err := db.InsertPerson(ctx, PersonRecord{
		Name:  "Null Scan Test Person",
		Email: "null-scan-test@example.com",
	})
	if err != nil {
		t.Fatalf("InsertPerson: %v", err)
	}
	// person_id FK cascades, so this also deletes the projects below.
	t.Cleanup(func() { _ = db.ClearPerson(ctx, personID) })

	// Insert a project with BOTH description and url as SQL NULL. InsertProject
	// binds Go strings ('' not NULL), so we go through the pool directly to
	// reproduce the real prod row shape.
	if _, err := db.pool.Exec(ctx,
		`INSERT INTO resume_projects (person_id, name, description, url, tech, highlights)
		 VALUES ($1, $2, NULL, NULL, NULL, NULL)`,
		personID, "null-cols-project",
	); err != nil {
		t.Fatalf("insert NULL project: %v", err)
	}
	// And a normal fully-populated project alongside it.
	if _, err := db.InsertProject(ctx, personID, ProjectRecord{
		Name:        "populated-project",
		Description: "has a description",
		URL:         "https://example.com",
		Tech:        []string{"Go"},
		Highlights:  []string{"shipped"},
	}); err != nil {
		t.Fatalf("InsertProject: %v", err)
	}

	// 1) The scan path itself must not error on the NULL row.
	records, err := db.GetAllProjects(ctx, personID)
	if err != nil {
		t.Fatalf("GetAllProjects returned error on NULL description/url (the bug): %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("GetAllProjects: got %d projects, want 2 (NULL row must not vanish)", len(records))
	}
	for _, r := range records {
		if r.Name == "null-cols-project" {
			if r.Description != "" || r.URL != "" {
				t.Errorf("NULL columns must scan as empty string, got desc=%q url=%q", r.Description, r.URL)
			}
		}
	}

	// 2) The real profile load helper must surface both projects (the symptom
	// the operator saw was total_projects: 0).
	summaries := loadProjects(ctx, db, personID)
	if len(summaries) != 2 {
		t.Fatalf("loadProjects: got %d projects, want 2", len(summaries))
	}
}

// TestGetProjectsByIDs_NullDescriptionAndURL guards the sibling getter that
// shares the same scan shape.
func TestGetProjectsByIDs_NullDescriptionAndURL(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	dbtest.RequireTestDB(t, dsn)

	ctx := context.Background()
	db, err := ConnectResumeDB(ctx, dsn)
	if err != nil {
		t.Fatalf("ConnectResumeDB: %v", err)
	}
	t.Cleanup(db.Close)

	personID, err := db.InsertPerson(ctx, PersonRecord{
		Name:  "Null Scan ByIDs Person",
		Email: "null-scan-byids@example.com",
	})
	if err != nil {
		t.Fatalf("InsertPerson: %v", err)
	}
	t.Cleanup(func() { _ = db.ClearPerson(ctx, personID) })

	var projID int
	if err := db.pool.QueryRow(ctx,
		`INSERT INTO resume_projects (person_id, name, description, url, tech, highlights)
		 VALUES ($1, $2, NULL, NULL, NULL, NULL) RETURNING id`,
		personID, "null-cols-byid",
	).Scan(&projID); err != nil {
		t.Fatalf("insert NULL project: %v", err)
	}

	records, err := db.GetProjectsByIDs(ctx, []int{projID})
	if err != nil {
		t.Fatalf("GetProjectsByIDs returned error on NULL description/url: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("GetProjectsByIDs: got %d, want 1", len(records))
	}
}
