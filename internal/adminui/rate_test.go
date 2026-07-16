package adminui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestValidStageAllowlists verifies validPipelineStages and validTriageStages
// after the migration 012 split. Each axis has its own closed-enum allowlist.
//
// Red-on-revert: remove either map or move a value between axes → mismatches here.
func TestValidStageAllowlists(t *testing.T) {
	pipelineCases := []struct {
		stage string
		want  bool
	}{
		// Pipeline-axis values (hunt_ratings.stage).
		{"claimed", true},
		{"applied", true},
		{"interview", true},
		{"offer", true},
		{"rejected", true},
		// Triage values must NOT be in the pipeline allowlist.
		{"interesting", false},
		{"saved", false},
		{"discarded", false},
		// Legacy / invalid.
		{"new", false},
		{"hacked", false},
		{"", false},
		{"APPLIED", false},
	}
	for _, tc := range pipelineCases {
		got := validPipelineStages[tc.stage]
		if got != tc.want {
			t.Errorf("validPipelineStages[%q] = %v, want %v", tc.stage, got, tc.want)
		}
	}

	triageCases := []struct {
		stage string
		want  bool
	}{
		// Triage-axis values (hunt_ratings.triage).
		{"interesting", true},
		{"saved", true},
		{"discarded", true},
		// Pipeline values must NOT be in the triage allowlist.
		{"claimed", false},
		{"applied", false},
		{"interview", false},
		{"offer", false},
		{"rejected", false},
		// Legacy / invalid.
		{"new", false},
		{"hacked", false},
		{"", false},
		{"SAVED", false},
	}
	for _, tc := range triageCases {
		got := validTriageStages[tc.stage]
		if got != tc.want {
			t.Errorf("validTriageStages[%q] = %v, want %v", tc.stage, got, tc.want)
		}
	}
}

// TestRateHandler_BadID verifies that a non-numeric id returns 400.
func TestRateHandler_BadID(t *testing.T) {
	handler := rateHandler(nil, "admin")

	form := url.Values{}
	form.Set("stage", "saved")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/admin/jobs/abc/rate",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "abc")

	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400 for bad id, got %d", rr.Code)
	}
}
