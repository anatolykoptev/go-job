package adminui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/go-panel/auth"
	"github.com/anatolykoptev/go-panel/resource"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testDetailAuth returns a throwaway auth for unit tests.
func testDetailAuth() *auth.HMACAuth {
	return auth.NewHMACAuth(auth.HMACConfig{
		Username:   "admin",
		Password:   "pw",
		HMACKey:    []byte("00000000000000000000000000000000"),
		BasePath:   "/admin",
		SessionTTL: time.Hour,
	})
}

// testDetailPanel returns a minimal resource.Panel for unit tests.
func testDetailPanel() *resource.Panel {
	a := testDetailAuth()
	return resource.New(resource.Config{
		Title:    "go-job-test",
		BasePath: "/admin",
		Auth:     a,
		CSRFKey:  []byte("00000000000000000000000000000000"),
	})
}

// TestJobDetailer_Smoke runs jobDetailer against DATABASE_URL.
// Skips when DATABASE_URL is unset (CI-safe). Fetches the first job's id and
// verifies the Detailer returns the expected sections.
func TestJobDetailer_Smoke(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping job detailer integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	csrfKey := []byte("00000000000000000000000000000000")
	hs := hunt.NewStore(pool)
	a := testDetailAuth()
	detailer := jobDetailer(pool, hs, "admin", a, csrfKey, t.TempDir())

	t.Run("existing_id_section_shapes", func(t *testing.T) {
		var id int64
		if scanErr := pool.QueryRow(context.Background(), "SELECT id FROM hunt_jobs LIMIT 1").Scan(&id); scanErr != nil {
			t.Skip("no rows in hunt_jobs — skipping")
		}

		// Build a real request so sessionValue can read (or not find) the cookie.
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/admin/jobs/"+strconv.FormatInt(id, 10), nil)

		sections, err := detailer(context.Background(), req, strconv.FormatInt(id, 10))
		if err != nil {
			t.Fatalf("detailer returned error: %v", err)
		}
		if len(sections) < 3 {
			t.Fatalf("want ≥3 sections, got %d", len(sections))
		}

		// Section 0 must be the CSS styles RawHTML.
		if !strings.Contains(sections[0].RawHTML, "<style>") {
			t.Errorf("section[0] should be CSS styles block, got title=%q", sections[0].Title)
		}

		// Find the Overview section (Title == "Overview").
		var overviewFound bool
		for _, s := range sections {
			if s.Title == "Overview" {
				overviewFound = true
				if len(s.Items) == 0 {
					t.Errorf("Overview section has no items")
				}
			}
		}
		if !overviewFound {
			t.Error("no Overview section found")
		}

		// Find the Application section (Title == "Application") and verify _csrf field.
		var appFound bool
		for _, s := range sections {
			if s.Title == "Application" {
				appFound = true
				if !strings.Contains(s.RawHTML, `name="_csrf"`) {
					t.Errorf("Application section RawHTML missing name=\"_csrf\": %s", s.RawHTML[:min(200, len(s.RawHTML))])
				}
			}
		}
		if !appFound {
			t.Error("no Application section found")
		}

		t.Logf("detailer OK: id=%d, sections=%d", id, len(sections))
	})

	t.Run("missing_id_returns_ErrDetailNotFound", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/admin/jobs/999999999", nil)

		_, err := detailer(context.Background(), req, "999999999")
		if err == nil {
			t.Fatal("want error for missing id, got nil")
		}
		if !isJobDetailNotFound(err) {
			t.Fatalf("want ErrDetailNotFound, got %v", err)
		}
	})

	t.Run("bad_id_returns_ErrDetailNotFound", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/admin/jobs/abc", nil)

		_, err := detailer(context.Background(), req, "abc")
		if err == nil {
			t.Fatal("want error for bad id, got nil")
		}
		if !isJobDetailNotFound(err) {
			t.Fatalf("want ErrDetailNotFound, got %v", err)
		}
	})
}

// TestJobDetailer_ApplicationSection_CSRF verifies that buildApplicationSectionHTML
// produces a form with a _csrf field. This test does not require a database.
func TestJobDetailer_ApplicationSection_CSRF(t *testing.T) {
	const fakeTok = "test-csrf-token-abc123"
	html, err := buildApplicationSectionHTML(42, fakeTok, nil, false, false)
	if err != nil {
		t.Fatalf("buildApplicationSectionHTML: %v", err)
	}
	if !strings.Contains(html, `name="_csrf"`) {
		t.Errorf("missing name=\"_csrf\" in application HTML")
	}
	if !strings.Contains(html, fakeTok) {
		t.Errorf("missing CSRF token value %q in application HTML", fakeTok)
	}
	if !strings.Contains(html, `/admin/jobs/42/rate`) {
		t.Errorf("missing rate form action in application HTML")
	}
}

// TestJobDetailer_ApplicationSection_CSRF_WithRating verifies _csrf is present
// when a current rating exists (different template branch).
func TestJobDetailer_ApplicationSection_CSRF_WithRating(t *testing.T) {
	const fakeTok = "test-csrf-token-xyz789"
	rating := &currentRating{Stage: "interesting", Note: "looks good"}
	html, err := buildApplicationSectionHTML(99, fakeTok, rating, false, false)
	if err != nil {
		t.Fatalf("buildApplicationSectionHTML with rating: %v", err)
	}
	if !strings.Contains(html, `name="_csrf"`) {
		t.Errorf("missing name=\"_csrf\" in application HTML with rating")
	}
	if !strings.Contains(html, fakeTok) {
		t.Errorf("missing CSRF token value %q in application HTML with rating", fakeTok)
	}
	if !strings.Contains(html, "interesting") {
		t.Errorf("missing rating.Stage in application HTML with rating")
	}
}

// TestJobDetailer_ErrDetailNotFound_RedCoverage verifies that the test itself
// fails when the guard is broken. This is the anti-vacuous check: if
// ErrDetailNotFound is not returned by the detailer, the test must go RED.
//
// We verify this indirectly: isJobDetailNotFound(nil) must return false,
// and isJobDetailNotFound(resource.ErrDetailNotFound) must return true.
func TestJobDetailer_ErrDetailNotFound_RedCoverage(t *testing.T) {
	if isJobDetailNotFound(nil) {
		t.Fatal("isJobDetailNotFound(nil) must return false — broken guard")
	}
	if !isJobDetailNotFound(resource.ErrDetailNotFound) {
		t.Fatal("isJobDetailNotFound(ErrDetailNotFound) must return true")
	}
}

// TestBuildOverviewSection_FieldsPresent verifies that buildOverviewSection
// includes Remote, Type, and Experience items when the fields are non-empty.
// This is a pure unit test — no database required.
func TestBuildOverviewSection_FieldsPresent(t *testing.T) {
	rec := jobDetailRecord{
		Company:    "Acme Corp",
		Location:   "San Francisco",
		Remote:     "yes",
		JobType:    "full-time",
		Experience: "5+ years",
		Status:     "new",
	}
	sec := buildOverviewSection(rec, "")
	if sec.Title != "Overview" {
		t.Fatalf("expected title Overview, got %q", sec.Title)
	}
	want := map[string]string{
		"Company":    "Acme Corp",
		"Location":   "San Francisco",
		"Remote":     "yes",
		"Type":       "full-time",
		"Experience": "5+ years",
		"Status":     "new",
	}
	got := make(map[string]string, len(sec.Items))
	for _, item := range sec.Items {
		got[item.Label] = item.Value
	}
	for label, wantVal := range want {
		if gotVal, ok := got[label]; !ok {
			t.Errorf("missing item %q in Overview", label)
		} else if gotVal != wantVal {
			t.Errorf("item %q: got %q, want %q", label, gotVal, wantVal)
		}
	}
}

// isJobDetailNotFound is a test helper that wraps the resource package check.
func isJobDetailNotFound(err error) bool {
	return err != nil && err.Error() == resource.ErrDetailNotFound.Error()
}
