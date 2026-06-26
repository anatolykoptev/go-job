package adminui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/anatolykoptev/go-panel/csrf"
)

// TestResumeProjectDeleteHandler_CSRFReject asserts POST /admin/resume/project/{id}/delete
// returns 403 when the CSRF token is missing.
// Red-on-revert: remove verifyCSRF call in resumeProjectDeleteHandler → returns 400/500.
func TestResumeProjectDeleteHandler_CSRFReject(t *testing.T) {
	key := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	a := buildTestAuth(key)
	handler := resumeProjectDeleteHandler(a, key)

	form := url.Values{}
	// _csrf omitted → expect 403
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
		"/admin/resume/project/1/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "1")

	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("want 403 for missing CSRF, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestResumeProjectDeleteHandler_BadID asserts POST /admin/resume/project/{id}/delete
// returns 400 for a non-numeric id with a valid CSRF token.
// Red-on-revert: remove parseIDParam call → nil pointer on DB call.
func TestResumeProjectDeleteHandler_BadID(t *testing.T) {
	key := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	a := buildTestAuth(key)
	handler := resumeProjectDeleteHandler(a, key)

	tok := csrf.Issue(key, "", csrf.DefaultTTL)
	form := url.Values{}
	form.Set(csrf.FormField, tok)
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

// TestResumeEducationDeleteHandler_CSRFReject asserts POST /admin/resume/education/{id}/delete
// returns 403 when the CSRF token is missing.
// Red-on-revert: remove verifyCSRF call in resumeEducationDeleteHandler → returns 400/500.
func TestResumeEducationDeleteHandler_CSRFReject(t *testing.T) {
	key := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	a := buildTestAuth(key)
	handler := resumeEducationDeleteHandler(a, key)

	form := url.Values{}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
		"/admin/resume/education/1/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "1")

	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("want 403 for missing CSRF, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestResumeEducationDeleteHandler_BadID asserts POST /admin/resume/education/{id}/delete
// returns 400 for a non-numeric id with a valid CSRF token.
func TestResumeEducationDeleteHandler_BadID(t *testing.T) {
	key := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	a := buildTestAuth(key)
	handler := resumeEducationDeleteHandler(a, key)

	tok := csrf.Issue(key, "", csrf.DefaultTTL)
	form := url.Values{}
	form.Set(csrf.FormField, tok)
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

// TestResumeCertificationDeleteHandler_CSRFReject asserts POST /admin/resume/certification/{id}/delete
// returns 403 when the CSRF token is missing.
// Red-on-revert: remove verifyCSRF call in resumeCertificationDeleteHandler → returns 400/500.
func TestResumeCertificationDeleteHandler_CSRFReject(t *testing.T) {
	key := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	a := buildTestAuth(key)
	handler := resumeCertificationDeleteHandler(a, key)

	form := url.Values{}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
		"/admin/resume/certification/1/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "1")

	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("want 403 for missing CSRF, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestResumeCertificationDeleteHandler_BadID asserts POST /admin/resume/certification/{id}/delete
// returns 400 for a non-numeric id with a valid CSRF token.
func TestResumeCertificationDeleteHandler_BadID(t *testing.T) {
	key := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	a := buildTestAuth(key)
	handler := resumeCertificationDeleteHandler(a, key)

	tok := csrf.Issue(key, "", csrf.DefaultTTL)
	form := url.Values{}
	form.Set(csrf.FormField, tok)
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
