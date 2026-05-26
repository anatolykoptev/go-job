package jobs

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// resetTracker resets the singleton so each test gets a fresh DB.
// Uses GO_JOB_TRACKER_DB to pin the DB path to a temp file,
// avoiding any dependency on HOME or container read-only filesystem.
func resetTracker(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "tracker.db")
	t.Setenv("GO_JOB_TRACKER_DB", dbPath)
	// Clear any stale XDG/DATA_DIR overrides from parent env.
	t.Setenv("GO_JOB_DATA_DIR", "")
	t.Setenv("XDG_DATA_HOME", "")
	// Reset the singleton.
	trackerDB = nil
	trackerErr = nil
	trackerOnce = sync.Once{}
	return dbPath
}

func TestAddTrackedJob_Basic(t *testing.T) {
	resetTracker(t)
	ctx := context.Background()

	result, err := AddTrackedJob(ctx, JobTrackerAddInput{
		Title:   "Senior Go Developer",
		Company: "Stripe",
		URL:     "https://stripe.com/jobs/123",
		Status:  "applied",
		Notes:   "Applied via LinkedIn",
		Salary:  "$180k",
		Location: "Remote",
	})
	if err != nil {
		t.Fatalf("AddTrackedJob error: %v", err)
	}
	if result.ID <= 0 {
		t.Errorf("expected positive ID, got %d", result.ID)
	}
	if result.Message == "" {
		t.Error("expected non-empty message")
	}
}

func TestAddTrackedJob_DefaultStatus(t *testing.T) {
	resetTracker(t)
	ctx := context.Background()

	result, err := AddTrackedJob(ctx, JobTrackerAddInput{
		Title:   "Backend Engineer",
		Company: "Acme",
	})
	if err != nil {
		t.Fatalf("AddTrackedJob error: %v", err)
	}
	if result.ID <= 0 {
		t.Errorf("expected positive ID, got %d", result.ID)
	}
}

func TestAddTrackedJob_MissingRequired(t *testing.T) {
	resetTracker(t)
	ctx := context.Background()

	_, err := AddTrackedJob(ctx, JobTrackerAddInput{Title: "Only Title"})
	if err == nil {
		t.Error("expected error when company is missing")
	}

	_, err = AddTrackedJob(ctx, JobTrackerAddInput{Company: "Only Company"})
	if err == nil {
		t.Error("expected error when title is missing")
	}
}

func TestAddTrackedJob_InvalidStatus(t *testing.T) {
	resetTracker(t)
	ctx := context.Background()

	_, err := AddTrackedJob(ctx, JobTrackerAddInput{
		Title:   "Dev",
		Company: "Corp",
		Status:  "unknown_status",
	})
	if err == nil {
		t.Error("expected error for invalid status")
	}
}

func TestListTrackedJobs_Empty(t *testing.T) {
	resetTracker(t)
	ctx := context.Background()

	result, err := ListTrackedJobs(ctx, JobTrackerListInput{})
	if err != nil {
		t.Fatalf("ListTrackedJobs error: %v", err)
	}
	if result.Total != 0 {
		t.Errorf("expected 0 total, got %d", result.Total)
	}
	if len(result.Jobs) != 0 {
		t.Errorf("expected 0 jobs, got %d", len(result.Jobs))
	}
}

func TestListTrackedJobs_WithData(t *testing.T) {
	resetTracker(t)
	ctx := context.Background()

	// Add 3 jobs with different statuses.
	for _, tc := range []struct {
		title, company, status string
	}{
		{"Go Dev", "Stripe", "applied"},
		{"Python Dev", "Google", "interview"},
		{"Rust Dev", "Mozilla", "saved"},
	} {
		_, err := AddTrackedJob(ctx, JobTrackerAddInput{
			Title: tc.title, Company: tc.company, Status: tc.status,
		})
		if err != nil {
			t.Fatalf("AddTrackedJob error: %v", err)
		}
	}

	// List all.
	all, err := ListTrackedJobs(ctx, JobTrackerListInput{})
	if err != nil {
		t.Fatalf("ListTrackedJobs error: %v", err)
	}
	if all.Total != 3 {
		t.Errorf("total = %d, want 3", all.Total)
	}
	if len(all.Jobs) != 3 {
		t.Errorf("jobs len = %d, want 3", len(all.Jobs))
	}

	// Filter by status.
	applied, err := ListTrackedJobs(ctx, JobTrackerListInput{Status: "applied"})
	if err != nil {
		t.Fatalf("ListTrackedJobs filter error: %v", err)
	}
	if applied.Total != 1 {
		t.Errorf("applied total = %d, want 1", applied.Total)
	}
	if applied.Jobs[0].Title != "Go Dev" {
		t.Errorf("applied job title = %q, want 'Go Dev'", applied.Jobs[0].Title)
	}
}

func TestListTrackedJobs_InvalidStatus(t *testing.T) {
	resetTracker(t)
	ctx := context.Background()

	_, err := ListTrackedJobs(ctx, JobTrackerListInput{Status: "bogus"})
	if err == nil {
		t.Error("expected error for invalid status filter")
	}
}

func TestListTrackedJobs_DefaultLimit(t *testing.T) {
	resetTracker(t)
	ctx := context.Background()

	// Limit=0 should default to 50.
	result, err := ListTrackedJobs(ctx, JobTrackerListInput{Limit: 0})
	if err != nil {
		t.Fatalf("ListTrackedJobs error: %v", err)
	}
	if result.Jobs == nil {
		t.Error("jobs should not be nil")
	}
}

func TestUpdateTrackedJob_Status(t *testing.T) {
	resetTracker(t)
	ctx := context.Background()

	added, err := AddTrackedJob(ctx, JobTrackerAddInput{
		Title: "Dev", Company: "Corp", Status: "saved",
	})
	if err != nil {
		t.Fatalf("AddTrackedJob error: %v", err)
	}

	result, err := UpdateTrackedJob(ctx, JobTrackerUpdateInput{
		ID:     added.ID,
		Status: "applied",
	})
	if err != nil {
		t.Fatalf("UpdateTrackedJob error: %v", err)
	}
	if result.ID != added.ID {
		t.Errorf("result ID = %d, want %d", result.ID, added.ID)
	}

	// Verify status changed.
	list, _ := ListTrackedJobs(ctx, JobTrackerListInput{Status: "applied"})
	if list.Total != 1 {
		t.Errorf("expected 1 applied job after update, got %d", list.Total)
	}
}

func TestUpdateTrackedJob_Notes(t *testing.T) {
	resetTracker(t)
	ctx := context.Background()

	added, _ := AddTrackedJob(ctx, JobTrackerAddInput{Title: "Dev", Company: "Corp"})

	_, err := UpdateTrackedJob(ctx, JobTrackerUpdateInput{
		ID:    added.ID,
		Notes: "Interview on March 1st",
	})
	if err != nil {
		t.Fatalf("UpdateTrackedJob notes error: %v", err)
	}
}

func TestUpdateTrackedJob_StatusAndNotes(t *testing.T) {
	resetTracker(t)
	ctx := context.Background()

	added, _ := AddTrackedJob(ctx, JobTrackerAddInput{Title: "Dev", Company: "Corp"})

	_, err := UpdateTrackedJob(ctx, JobTrackerUpdateInput{
		ID:     added.ID,
		Status: "interview",
		Notes:  "Technical round scheduled",
	})
	if err != nil {
		t.Fatalf("UpdateTrackedJob error: %v", err)
	}
}

func TestUpdateTrackedJob_InvalidID(t *testing.T) {
	resetTracker(t)
	ctx := context.Background()

	_, err := UpdateTrackedJob(ctx, JobTrackerUpdateInput{ID: 0, Status: "applied"})
	if err == nil {
		t.Error("expected error for ID=0")
	}
}

func TestUpdateTrackedJob_NoFields(t *testing.T) {
	resetTracker(t)
	ctx := context.Background()

	_, err := UpdateTrackedJob(ctx, JobTrackerUpdateInput{ID: 1})
	if err == nil {
		t.Error("expected error when neither status nor notes provided")
	}
}

func TestUpdateTrackedJob_InvalidStatus(t *testing.T) {
	resetTracker(t)
	ctx := context.Background()

	added, _ := AddTrackedJob(ctx, JobTrackerAddInput{Title: "Dev", Company: "Corp"})

	_, err := UpdateTrackedJob(ctx, JobTrackerUpdateInput{
		ID:     added.ID,
		Status: "bad_status",
	})
	if err == nil {
		t.Error("expected error for invalid status in update")
	}
}

func TestValidStatus(t *testing.T) {
	valid := []string{"saved", "applied", "interview", "offer", "rejected"}
	for _, s := range valid {
		if !validStatus(s) {
			t.Errorf("validStatus(%q) = false, want true", s)
		}
	}
	invalid := []string{"", "pending", "APPLIED", "done", "closed"}
	for _, s := range invalid {
		if validStatus(s) {
			t.Errorf("validStatus(%q) = true, want false", s)
		}
	}
}

// TestResolveTrackerDir verifies env-variable path priority.
func TestResolveTrackerDir(t *testing.T) {
	// Clear all overrides before each sub-test.
	clearTrackerEnv := func(t *testing.T) {
		t.Helper()
		t.Setenv("GO_JOB_TRACKER_DB", "")
		t.Setenv("GO_JOB_DATA_DIR", "")
		t.Setenv("XDG_DATA_HOME", "")
	}

	t.Run("GO_JOB_TRACKER_DB takes priority", func(t *testing.T) {
		clearTrackerEnv(t)
		dbPath := filepath.Join(t.TempDir(), "custom.db")
		t.Setenv("GO_JOB_TRACKER_DB", dbPath)
		got, err := resolveTrackerDir()
		if err != nil {
			t.Fatalf("resolveTrackerDir error: %v", err)
		}
		if got != filepath.Dir(dbPath) {
			t.Errorf("got %q, want %q", got, filepath.Dir(dbPath))
		}
	})

	t.Run("GO_JOB_DATA_DIR second priority", func(t *testing.T) {
		clearTrackerEnv(t)
		dataDir := t.TempDir()
		t.Setenv("GO_JOB_DATA_DIR", dataDir)
		got, err := resolveTrackerDir()
		if err != nil {
			t.Fatalf("resolveTrackerDir error: %v", err)
		}
		want := filepath.Join(dataDir, "tracker")
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("XDG_DATA_HOME third priority", func(t *testing.T) {
		clearTrackerEnv(t)
		xdg := t.TempDir()
		t.Setenv("XDG_DATA_HOME", xdg)
		got, err := resolveTrackerDir()
		if err != nil {
			t.Fatalf("resolveTrackerDir error: %v", err)
		}
		want := filepath.Join(xdg, "go-job")
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

// TestOpenTrackerDB_EnvPath verifies that GO_JOB_TRACKER_DB creates the DB
// at the specified exact path (not an appended tracker.db inside it).
func TestOpenTrackerDB_EnvPath(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "explicit.db")
	t.Setenv("GO_JOB_TRACKER_DB", dbPath)
	t.Setenv("GO_JOB_DATA_DIR", "")
	t.Setenv("XDG_DATA_HOME", "")
	trackerDB = nil
	trackerErr = nil
	trackerOnce = sync.Once{}

	ctx := context.Background()
	_, err := AddTrackedJob(ctx, JobTrackerAddInput{Title: "T", Company: "C"})
	if err != nil {
		t.Fatalf("AddTrackedJob error: %v", err)
	}

	stat, statErr := os.Stat(dbPath)
	if statErr != nil {
		t.Fatalf("tracker DB file not created at expected path %s: %v", dbPath, statErr)
	}
	if !stat.Mode().IsRegular() {
		t.Errorf("expected regular file at %s, got mode %v", dbPath, stat.Mode())
	}
	// Verify it's the exact path by listing and ensuring no tracker.db sibling.
	entries, _ := filepath.Glob(filepath.Join(filepath.Dir(dbPath), "tracker.db"))
	if len(entries) > 0 {
		t.Errorf("unexpected tracker.db sibling created alongside explicit path")
	}
}

// TestOpenTrackerDB_FallsBackOnReadOnlyHome verifies that openTrackerDB falls back
// to os.TempDir() when the XDG_DATA_HOME-derived path is on a read-only filesystem.
// This reproduces the container scenario where HOME=/root and /root is read-only.
func TestOpenTrackerDB_FallsBackOnReadOnlyHome(t *testing.T) {
	// Create a directory we immediately make read-only to simulate EROFS.
	readOnlyParent := t.TempDir()
	if err := os.Chmod(readOnlyParent, 0555); err != nil {
		t.Skipf("cannot chmod dir read-only: %v", err)
	}
	t.Cleanup(func() {
		// Restore so t.TempDir cleanup can remove it.
		_ = os.Chmod(readOnlyParent, 0755)
	})

	// Clear all env overrides; point XDG_DATA_HOME at the read-only dir.
	t.Setenv("GO_JOB_TRACKER_DB", "")
	t.Setenv("GO_JOB_DATA_DIR", "")
	t.Setenv("XDG_DATA_HOME", readOnlyParent)

	// Reset singleton.
	trackerDB = nil
	trackerErr = nil
	trackerOnce = sync.Once{}

	ctx := context.Background()
	_, err := AddTrackedJob(ctx, JobTrackerAddInput{Title: "Fallback Job", Company: "Corp"})
	if err != nil {
		t.Fatalf("AddTrackedJob should succeed via tempdir fallback, got: %v", err)
	}

	// Verify the DB landed under os.TempDir(), not under the read-only dir.
	if trackerDB == nil {
		t.Fatal("trackerDB is nil after successful AddTrackedJob")
	}
	// Confirm go-job subdir exists under TempDir.
	expectedFallbackDir := filepath.Join(os.TempDir(), "go-job")
	stat, statErr := os.Stat(expectedFallbackDir)
	if statErr != nil {
		t.Fatalf("fallback dir %s not created: %v", expectedFallbackDir, statErr)
	}
	if !stat.IsDir() {
		t.Errorf("expected dir at fallback path %s", expectedFallbackDir)
	}
}

func TestInitTrackerSchema_Idempotent(t *testing.T) {
	dbPath := resetTracker(t)
	ctx := context.Background()

	// Open DB twice — schema init should be idempotent.
	_, err := AddTrackedJob(ctx, JobTrackerAddInput{Title: "A", Company: "B"})
	if err != nil {
		t.Fatalf("first add error: %v", err)
	}

	// Reset singleton but keep same DB path (same file, re-open).
	trackerDB = nil
	trackerErr = nil
	trackerOnce = sync.Once{}
	t.Setenv("GO_JOB_TRACKER_DB", dbPath)

	_, err = AddTrackedJob(ctx, JobTrackerAddInput{Title: "C", Company: "D"})
	if err != nil {
		t.Fatalf("second add after re-open error: %v", err)
	}

	list, _ := ListTrackedJobs(ctx, JobTrackerListInput{})
	if list.Total != 2 {
		t.Errorf("expected 2 total after re-open, got %d", list.Total)
	}
}
