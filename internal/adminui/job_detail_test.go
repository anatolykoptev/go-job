package adminui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestJobDetailHandler_Smoke runs jobDetailHandler against DATABASE_URL.
// Skips when DATABASE_URL is unset (CI-safe). Fetches the first job's id and
// expects a 200 with the detail page HTML. Also tests 404 on non-existent id.
func TestJobDetailHandler_Smoke(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping job detail integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	handler := jobDetailHandler(pool)

	t.Run("existing_id", func(t *testing.T) {
		var id int64
		if scanErr := pool.QueryRow(context.Background(), "SELECT id FROM hunt_jobs LIMIT 1").Scan(&id); scanErr != nil {
			t.Skip("no rows in hunt_jobs — skipping")
		}

		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/admin/jobs/1/view", nil)
		req.SetPathValue("id", strconv.FormatInt(id, 10))

		rr := httptest.NewRecorder()
		handler(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
		}
		body := rr.Body.String()
		if len(body) < 100 {
			t.Fatalf("response too short (%d bytes)", len(body))
		}
		t.Logf("job detail OK: id=%d, body=%d bytes", id, len(body))
	})

	t.Run("missing_id", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/admin/jobs/999999999/view", nil)
		req.SetPathValue("id", "999999999")

		rr := httptest.NewRecorder()
		handler(rr, req)

		if rr.Code != http.StatusNotFound {
			t.Fatalf("want 404, got %d", rr.Code)
		}
	})

	t.Run("bad_id", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/admin/jobs/abc/view", nil)
		req.SetPathValue("id", "abc")

		rr := httptest.NewRecorder()
		handler(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d", rr.Code)
		}
	})
}
