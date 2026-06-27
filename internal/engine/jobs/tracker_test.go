package jobs_test

import (
	"encoding/json"
	"testing"

	"github.com/anatolykoptev/go_job/internal/engine/jobs"
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
