package adminui

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/anatolykoptev/go-panel/resource"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestJobsLister_Smoke runs the jobs Lister against DATABASE_URL (a read-only
// SELECT) and asserts the generated SQL (OrderBy + column mapping + scan) is
// valid and that every row's cell count matches the spec. Skips when
// DATABASE_URL is unset, so it never connects to a DB in plain CI.
func TestJobsLister_Smoke(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping jobs lister integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	q := resource.ListQuery{
		Sort:   jobsSpec.Resolve("fit", "desc"),
		Limit:  25,
		Offset: 0,
	}
	rows, total, err := jobsLister(pool, "")(context.Background(), q)
	if err != nil {
		t.Fatalf("jobsLister: %v", err)
	}
	if total < 0 {
		t.Fatalf("negative total %d", total)
	}
	for i, r := range rows {
		if len(r.Cells) != len(jobsSpec.Columns) {
			t.Fatalf("row %d: %d cells, want %d", i, len(r.Cells), len(jobsSpec.Columns))
		}
	}
	t.Logf("jobs lister OK: %d rows (total=%d)", len(rows), total)
}

// TestJobsLister_PDFCells verifies that Resume and Cover cells show a PDF link when
// a prepared application pack exists and show "—" when none.
// Red-on-revert: remove pdfCells (or strip resumeCell/coverCell appends) and this
// test fails because cell count drops from 9 to 7 and link assertions are not reached.
func TestJobsLister_PDFCells(t *testing.T) {
	appsDir := t.TempDir()
	submitDir := appsDir + "/acme-platform/submit"
	if err := os.MkdirAll(submitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(submitDir+"/resume-acme.pdf", []byte("%PDF"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(appsDir)
	if err != nil {
		t.Fatal(err)
	}

	// Row WITH a matching pack (resume only).
	resumeCell, coverCell := pdfCells(42, "Acme", "Platform Engineer", appsDir, entries)
	if !resumeCell.HTML {
		t.Error("resumeCell.HTML should be true when PDF exists")
	}
	if !strings.Contains(resumeCell.Value, "/admin/jobs/42/download/resume") {
		t.Errorf("resumeCell.Value missing href: %q", resumeCell.Value)
	}
	if coverCell.Value != "—" {
		t.Errorf("coverCell should be dash when no cover PDF; got %q", coverCell.Value)
	}
	if coverCell.HTML {
		t.Error("coverCell.HTML should be false when no cover PDF")
	}

	// Row WITHOUT any matching pack.
	r2, c2 := pdfCells(99, "Nobody", "Engineer", appsDir, entries)
	if r2.Value != "—" || c2.Value != "—" {
		t.Errorf("expected dash,dash for unknown company; got %q, %q", r2.Value, c2.Value)
	}

	// Column count matches spec (regression guard).
	if len(jobsSpec.Columns) != 9 {
		t.Errorf("jobsSpec.Columns = %d, want 9", len(jobsSpec.Columns))
	}
}
