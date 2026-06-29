package jobs_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/anatolykoptev/go_job/internal/dbtest"
	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestJobTrackerContract_AddResult verifies that JobTrackerResult has the expected JSON shape.
func TestJobTrackerContract_AddResult(t *testing.T) {
	r := jobs.JobTrackerResult{ID: 42, Message: "test"}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, field := range []string{"id", "message"} {
		if _, ok := m[field]; !ok {
			t.Errorf("missing field %q in JobTrackerResult JSON", field)
		}
	}
}

// TestJobTrackerContract_ListResult verifies that JobTrackerListResult has the expected JSON shape.
func TestJobTrackerContract_ListResult(t *testing.T) {
	r := jobs.JobTrackerListResult{
		Jobs: []jobs.TrackedJob{
			{
				ID:        1,
				Title:     "Engineer",
				Company:   "Acme",
				URL:       "https://example.com/job",
				Status:    jobs.StatusApplied,
				Notes:     "great role",
				Salary:    "100k",
				Location:  "Remote",
				CreatedAt: "2026-01-01T00:00:00Z",
				UpdatedAt: "2026-01-02T00:00:00Z",
			},
		},
		Total: 1,
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, field := range []string{"jobs", "total"} {
		if _, ok := m[field]; !ok {
			t.Errorf("missing field %q in JobTrackerListResult JSON", field)
		}
	}
	jArr, ok := m["jobs"].([]any)
	if !ok || len(jArr) == 0 {
		t.Fatal("jobs field is not a non-empty array")
	}
	job, ok := jArr[0].(map[string]any)
	if !ok {
		t.Fatal("jobs[0] is not a map")
	}
	for _, field := range []string{"id", "title", "company", "url", "status", "created_at", "updated_at"} {
		if _, ok := job[field]; !ok {
			t.Errorf("missing field %q in TrackedJob JSON", field)
		}
	}
}

// TestTracker_SavedToApplied_TransitionVisibleInList exercises the FULL wired path:
//
//	AddTrackedJob(saved) → UpdateTrackedJob(applied) → ListTrackedJobs
//
// This is the CRITICAL regression guard for the trackerRate/RateExact wiring fix.
// The previous implementation called store.Rate() which preserves the inactive axis;
// a prior triage='saved' survived a pipeline update → trackerStatusFromRow returned
// 'saved' forever. The fix routes through store.RateExact, which unconditionally
// clears the inactive axis.
//
// Red-on-revert: change store.RateExact → store.Rate in trackerRate (tracker.go:140,142)
// → ListTrackedJobs returns status='saved' → assertion on wantStatus fails.
//
// Requires DATABASE_URL pointing to a *_test Postgres DB (skipped if absent).
func TestTracker_SavedToApplied_TransitionVisibleInList(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	dbtest.RequireTestDB(t, dsn)

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	store := hunt.NewStore(pool)
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Wire the engine singleton so AddTrackedJob/UpdateTrackedJob/ListTrackedJobs work.
	prev := engine.GetHuntStore()
	engine.SetHuntStore(store)
	t.Cleanup(func() { engine.SetHuntStore(prev) })

	ctx := context.Background()

	// Step 1: Add a tracker job as 'saved' (triage axis).
	addResult, err := jobs.AddTrackedJob(ctx, jobs.JobTrackerAddInput{
		Title:   "Tracker Transition Test Job",
		Company: "Acme Tracker Corp",
		URL:     "https://example.com/tracker-transition-" + t.Name(),
		Status:  "saved",
		Notes:   "initial note",
	})
	if err != nil {
		t.Fatalf("AddTrackedJob(saved): %v", err)
	}
	jobID := addResult.ID
	t.Logf("inserted tracker job id=%d", jobID)

	// Step 2: Update to 'applied' (pipeline axis). This is where the pre-fix code
	// would call Rate(triage="", stage="applied"), preserving the prior triage='saved'.
	if _, err := jobs.UpdateTrackedJob(ctx, jobs.JobTrackerUpdateInput{
		ID:     jobID,
		Status: "applied",
		Notes:  "applied note",
	}); err != nil {
		t.Fatalf("UpdateTrackedJob(applied): %v", err)
	}

	// Step 3: List and assert the reported status is 'applied', not 'saved'.
	listResult, err := jobs.ListTrackedJobs(ctx, jobs.JobTrackerListInput{Limit: 100})
	if err != nil {
		t.Fatalf("ListTrackedJobs: %v", err)
	}

	var found *jobs.TrackedJob
	for i := range listResult.Jobs {
		if listResult.Jobs[i].ID == jobID {
			found = &listResult.Jobs[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("job id=%d not found in ListTrackedJobs output (total=%d)", jobID, listResult.Total)
	}

	const wantStatus = jobs.StatusApplied
	if found.Status != wantStatus {
		t.Errorf("after Add(saved)→Update(applied): want status=%q, got %q — "+
			"this indicates trackerRate is using Rate() (CASE-preserve) instead of RateExact()", wantStatus, found.Status)
	}
}

// TestTrackedJob_JSONShape asserts exact JSON tag fidelity for TrackedJob.
func TestTrackedJob_JSONShape(t *testing.T) {
	j := jobs.TrackedJob{
		ID:        99,
		Title:     "t",
		Company:   "c",
		URL:       "u",
		Status:    jobs.StatusSaved,
		Notes:     "n",
		Salary:    "s",
		Location:  "l",
		CreatedAt: "2026-01-01T00:00:00Z",
		UpdatedAt: "2026-01-01T00:00:00Z",
	}
	b, _ := json.Marshal(j)
	var m map[string]any
	json.Unmarshal(b, &m) //nolint:errcheck

	wantKeys := []string{"id", "title", "company", "url", "status", "notes", "salary", "location", "created_at", "updated_at"}
	for _, k := range wantKeys {
		if _, ok := m[k]; !ok {
			t.Errorf("TrackedJob JSON missing key %q", k)
		}
	}
}
