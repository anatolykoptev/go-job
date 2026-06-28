package adminui

import (
	"context"
	"os"
	"testing"

	"github.com/anatolykoptev/go-panel/resource"
	"github.com/anatolykoptev/go_job/internal/dbtest"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestHuntResources_Smoke runs each hunt resource Lister against DATABASE_URL
// (read-only SELECT) and asserts the SQL builds and every row's cell count
// matches the spec columns. Skips when DATABASE_URL is unset (CI-safe);
// fatals if it points at a non-_test database.
func TestHuntResources_Smoke(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	dbtest.RequireTestDB(t, dsn)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	for _, r := range []resource.Resource{
		bountiesResource(pool), freelanceResource(pool), securityResource(pool), contestsResource(pool),
	} {
		q := resource.ListQuery{Sort: r.Sort.Resolve("", ""), Limit: 25}
		rows, total, err := r.Lister(context.Background(), q)
		if err != nil {
			t.Fatalf("%s lister: %v", r.Name, err)
		}
		for i, row := range rows {
			if len(row.Cells) != len(r.Sort.Columns) {
				t.Fatalf("%s row %d: %d cells, want %d", r.Name, i, len(row.Cells), len(r.Sort.Columns))
			}
		}
		t.Logf("%s: %d rows (total=%d)", r.Name, len(rows), total)
	}
}
