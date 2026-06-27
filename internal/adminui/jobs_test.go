package adminui

import (
	"context"
	"os"
	"testing"

	"github.com/anatolykoptev/go-panel/resource"
	"github.com/jackc/pgx/v5/pgxpool"
)

func openJobsPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping jobs lister integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	return pool
}

// TestJobsLister_Smoke runs the jobs Lister against DATABASE_URL (a read-only
// SELECT) and asserts the generated SQL (OrderBy + column mapping + scan) is
// valid and that every row's cell count matches the spec. Skips when
// DATABASE_URL is unset, so it never connects to a DB in plain CI.
func TestJobsLister_Smoke(t *testing.T) {
	pool := openJobsPool(t)
	q := resource.ListQuery{
		Sort:   jobsSpec.Resolve("fit", "desc"),
		Limit:  25,
		Offset: 0,
	}
	// authority=nil: docs column renders empty chips (no crash).
	rows, total, err := jobsLister(pool, nil)(context.Background(), q)
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

// TestJobsLister_OffsetReturnsDistinctWindow is the pagination regression guard.
// It calls jobsLister with Offset=0 and Offset=pageSize and asserts the first-row
// IDs differ — RED-on-revert if LIMIT/OFFSET are removed from the SQL.
// Requires DATABASE_URL with ≥pageSize+1 rows in hunt_jobs; skips otherwise.
func TestJobsLister_OffsetReturnsDistinctWindow(t *testing.T) {
	const pageSize = 10
	pool := openJobsPool(t)

	var total int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM hunt_jobs").Scan(&total); err != nil {
		t.Fatalf("count hunt_jobs: %v", err)
	}
	if total < pageSize+1 {
		t.Skipf("not enough rows (%d) to test pagination (need ≥%d)", total, pageSize+1)
	}

	lister := jobsLister(pool, nil)
	sort := jobsSpec.Resolve("fit", "desc")

	page1, _, err := lister(context.Background(), resource.ListQuery{Sort: sort, Limit: pageSize, Offset: 0})
	if err != nil {
		t.Fatalf("lister(offset=0): %v", err)
	}
	page2, _, err := lister(context.Background(), resource.ListQuery{Sort: sort, Limit: pageSize, Offset: pageSize})
	if err != nil {
		t.Fatalf("lister(offset=%d): %v", pageSize, err)
	}
	if len(page1) == 0 || len(page2) == 0 {
		t.Fatalf("unexpected empty result: page1=%d rows, page2=%d rows", len(page1), len(page2))
	}
	if page1[0].ID == page2[0].ID {
		t.Errorf("pagination BROKEN: page1 and page2 share first row ID=%s — OFFSET not applied", page1[0].ID)
	}
}
