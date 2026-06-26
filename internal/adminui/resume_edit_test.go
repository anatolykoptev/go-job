package adminui

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/anatolykoptev/go-panel/csrf"
)

// TestResumeEditHandler_CSRFReject_Person asserts that POST /admin/resume/person
// returns 403 when the CSRF token is missing.
// Red-on-revert: remove verifyCSRF call in resumePersonEditHandler → returns 404/500.
func TestResumeEditHandler_CSRFReject_Person(t *testing.T) {
	key := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	a := buildTestAuth(key)
	handler := resumePersonEditHandler(a, key)

	form := url.Values{}
	form.Set("name", "Alice")
	// _csrf omitted → expect 403
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/admin/resume/person",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("want 403 for missing CSRF, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestResumeEditHandler_CSRFReject_Experience asserts POST /admin/resume/experience
// returns 403 on missing CSRF.
func TestResumeEditHandler_CSRFReject_Experience(t *testing.T) {
	key := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	a := buildTestAuth(key)
	handler := resumeExperienceCreateHandler(a, key)

	form := url.Values{"title": {"SWE"}, "company": {"Acme"}}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/admin/resume/experience",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("want 403, got %d", rr.Code)
	}
}

// TestResumeEditHandler_CSRFReject_Skill asserts POST /admin/resume/skill
// returns 403 on missing CSRF.
func TestResumeEditHandler_CSRFReject_Skill(t *testing.T) {
	key := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	a := buildTestAuth(key)
	handler := resumeSkillCreateHandler(a, key)

	form := url.Values{"name": {"Go"}}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/admin/resume/skill",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("want 403, got %d", rr.Code)
	}
}

// TestResumeEditHandler_CSRFReject_SkillLevel asserts POST /admin/resume/skill/{id}/level
// returns 403 on missing CSRF.
func TestResumeEditHandler_CSRFReject_SkillLevel(t *testing.T) {
	key := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	a := buildTestAuth(key)
	handler := resumeSkillLevelHandler(a, key)

	form := url.Values{"level": {"advanced"}}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/admin/resume/skill/1/level",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "1")

	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("want 403, got %d", rr.Code)
	}
}

// TestResumeEditHandler_BadID_Numeric asserts that id=0 returns 400
// (after a valid CSRF token passes verification, before any DB call).
// Red-on-revert: remove parseIDParam check → nil pointer on db.GetLatestPersonID.
func TestResumeEditHandler_BadID_Numeric(t *testing.T) {
	key := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	a := buildTestAuth(key)
	handler := resumeSkillDeleteHandler(a, key)

	tok := csrf.Issue(key, "", csrf.DefaultTTL)
	form := url.Values{}
	form.Set(csrf.FormField, tok)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
		"/admin/resume/skill/0/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "0")

	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400 for id=0, got %d", rr.Code)
	}
}

// TestResumeEditHandler_BadID_NonNumeric asserts that a non-numeric id returns 400.
func TestResumeEditHandler_BadID_NonNumeric(t *testing.T) {
	key := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	a := buildTestAuth(key)
	handler := resumeExperienceDeleteHandler(a, key)

	tok := csrf.Issue(key, "", csrf.DefaultTTL)
	form := url.Values{}
	form.Set(csrf.FormField, tok)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
		"/admin/resume/experience/abc/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "abc")

	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400 for non-numeric id, got %d", rr.Code)
	}
}

// TestResumeEditHandler_InvalidSkillLevel asserts that an invalid level returns 400.
// Red-on-revert: remove IsValidSkillLevel check → any level accepted silently.
func TestResumeEditHandler_InvalidSkillLevel(t *testing.T) {
	key := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	a := buildTestAuth(key)
	handler := resumeSkillLevelHandler(a, key)

	tok := csrf.Issue(key, "", csrf.DefaultTTL)
	form := url.Values{}
	form.Set(csrf.FormField, tok)
	form.Set("level", "master") // not in allowlist
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
		"/admin/resume/skill/1/level", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "1")

	rr := httptest.NewRecorder()
	handler(rr, req)

	// CSRF passes, id valid → invalid level must 400 before any DB call.
	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400 for invalid level, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestResumeEditTmpl_HasCSRFField asserts the template source includes hidden
// _csrf fields covering all editable sections.
// Red-on-revert: strip _csrf from resumeEditTmplSrc → test fails.
func TestResumeEditTmpl_HasCSRFField(t *testing.T) {
	const want = `name="_csrf"`
	if !strings.Contains(resumeEditTmplSrc, want) {
		t.Errorf("resumeEditTmplSrc missing %q — CSRF tokens must be present in all forms", want)
	}
	count := strings.Count(resumeEditTmplSrc, want)
	const minForms = 6 // person, exp-add, skill-add, ach-add, domain-add, meth-add + each delete form
	if count < minForms {
		t.Errorf("resumeEditTmplSrc has %d _csrf fields, want at least %d", count, minForms)
	}
}

// TestResumeEditTmpl_Parseable asserts the template compiles.
// Red-on-revert: introduce a syntax error in resumeEditTmplSrc → parse fails.
func TestResumeEditTmpl_Parseable(t *testing.T) {
	_, err := template.New("resume_edit").Parse(resumeEditTmplSrc)
	if err != nil {
		t.Errorf("resumeEditTmplSrc failed to parse: %v", err)
	}
}
