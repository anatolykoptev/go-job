package adminui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestResumeHandler_EmptyState verifies that the resume handler renders a
// 200 with a human-readable empty-state message when no resume data exists
// (GetLatestPersonID returns 0 / GetResumeDB returns nil). This guards
// against the handler panicking or returning 500 on a fresh deployment.
//
// The test does NOT require DATABASE_URL — it exercises the nil-DB branch.
func TestResumeHandler_EmptyState(t *testing.T) {
	// GetResumeDB() returns nil (package-level resumeDB is nil in test binary).
	// resumeHandler must detect nil and render the empty-state message gracefully.
	p := testDetailPanel()
	handler := resumeHandler(p)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/admin/resume", nil)
	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200 for empty state, got %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	// Must contain some expected empty-state signal, not a blank or panic body.
	if !strings.Contains(body, "database not configured") && !strings.Contains(body, "DATABASE_URL") {
		t.Fatalf("expected empty-state message in body, got %d bytes: %.200s", len(body), body)
	}
	// Falsification: if the nil-DB branch were removed, GetResumeDB() would panic
	// trying to call GetLatestPersonID on nil — the test would 500 or panic, not 200.
	t.Logf("empty-state OK: %d bytes", len(body))
}
