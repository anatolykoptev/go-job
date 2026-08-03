package adminui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestResumeProjectDeleteHandler_BadID asserts POST /admin/resume/project/{id}/delete
// returns 400 for a non-numeric id.
// Red-on-revert: remove parseIDParam call → nil pointer on DB call.
func TestResumeProjectDeleteHandler_BadID(t *testing.T) {
	handler := resumeProjectDeleteHandler()

	form := url.Values{}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
		"/admin/resume/project/abc/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "abc")

	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400 for non-numeric id, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestResumeEducationDeleteHandler_BadID asserts POST /admin/resume/education/{id}/delete
// returns 400 for a non-numeric id.
func TestResumeEducationDeleteHandler_BadID(t *testing.T) {
	handler := resumeEducationDeleteHandler()

	form := url.Values{}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
		"/admin/resume/education/abc/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "abc")

	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400 for non-numeric id, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestResumeCertificationDeleteHandler_BadID asserts POST /admin/resume/certification/{id}/delete
// returns 400 for a non-numeric id.
func TestResumeCertificationDeleteHandler_BadID(t *testing.T) {
	handler := resumeCertificationDeleteHandler()

	form := url.Values{}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
		"/admin/resume/certification/abc/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "abc")

	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400 for non-numeric id, got %d: %s", rr.Code, rr.Body.String())
	}
}
