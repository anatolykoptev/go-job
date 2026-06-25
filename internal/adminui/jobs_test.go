package adminui

import (
	"context"
	"os"
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
	rows, total, err := jobsLister(pool)(context.Background(), q)
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
