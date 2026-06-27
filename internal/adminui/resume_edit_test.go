package adminui

import (
	"bytes"
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/anatolykoptev/go-panel/csrf"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
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
// _csrf fields covering all editable sections (both add and delete forms).
// Red-on-revert: strip _csrf from resumeEditTmplSrc → test fails.
//
// Expected count (1 record of each type rendered):
//   - person-edit:           1  (1 form)
//   - experience-delete:     1  (1 row × delete form)
//   - experience-add:        1  (add form)
//   - skill-level:           1  (1 row × level form)
//   - skill-delete:          1  (1 row × delete form)
//   - skill-add:             1  (add form)
//   - achievement-delete:    1  (1 row × delete form)
//   - achievement-add:       1  (add form)
//   - domain-delete:         1  (1 row × delete form)
//   - domain-add:            1  (add form)
//   - methodology-delete:    1  (1 row × delete form)
//   - methodology-add:       1  (add form)
//   - project-delete:        1  (1 row × delete form)
//   - project-add:           1  (add form)
//   - education-delete:      1  (1 row × delete form)
//   - education-add:         1  (add form)
//   - certification-delete:  1  (1 row × delete form)
//   - certification-add:     1  (add form)
//
// Total static occurrences in template source: 18
// (all add forms + delete-per-row use {{$.CSRFToken}} or {{.CSRFToken}} identically)
func TestResumeEditTmpl_HasCSRFField(t *testing.T) {
	const want = `name="_csrf"`
	if !strings.Contains(resumeEditTmplSrc, want) {
		t.Errorf("resumeEditTmplSrc missing %q — CSRF tokens must be present in all forms", want)
	}
	count := strings.Count(resumeEditTmplSrc, want)
	// sections: person(1) + exp(del+add=2) + skill(level+del+add=3) + ach(del+add=2)
	//         + domain(del+add=2) + methodology(del+add=2) + project(del+add=2)
	//         + education(del+add=2) + certification(del+add=2) = 18
	const wantForms = 18
	if count != wantForms {
		t.Errorf("resumeEditTmplSrc has %d _csrf fields, want exactly %d — update this count when adding/removing forms", count, wantForms)
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

// TestResumeEditTmpl_RendersWithData executes resumeEditTmplSrc against a fully
// populated resumeEditData (one record of every type) and asserts:
//  1. No template execution error (catches field-name typos like {{.Naem}}).
//  2. A representative field from each record type appears in the output.
//
// Red-on-revert proof: rename any {{.Field}} reference in the template to a wrong
// name (e.g. {{.Naem}} instead of {{.Name}}) — the execution succeeds but the
// expected string does NOT appear in the output → this test goes RED.
// This guards the whole editor, including the new project/education/certification
// sections added in this PR.
func TestResumeEditTmpl_RendersWithData(t *testing.T) {
	tmpl := template.Must(template.New("resume_edit").Parse(resumeEditTmplSrc))

	teamSize := 5
	d := resumeEditData{
		CSRFToken: "test-csrf-token",
		Person: &jobs.PersonRecord{
			ID:       1,
			Name:     "Alice Engineer",
			Email:    "alice@example.com",
			Phone:    "+1-555-0100",
			Location: "San Francisco, CA",
			Summary:  "Senior software engineer specializing in distributed systems.",
		},
		Experiences: []jobs.ExperienceRecord{
			{
				ID:          10,
				PersonID:    1,
				Title:       "Staff Engineer",
				Company:     "Acme Corp",
				Location:    "Remote",
				StartDate:   "2020-01",
				EndDate:     "2023-12",
				Description: "Led platform team.",
				TeamSize:    &teamSize,
			},
		},
		Skills: []jobs.SkillRecord{
			{ID: 20, PersonID: 1, Name: "Go", Category: "languages", Level: "expert"},
		},
		Achievements: []jobs.AchievementRecord{
			{ID: 30, PersonID: 1, Text: "Reduced p99 latency by 40%", Metric: "latency", Value: "40%"},
		},
		Domains: []jobs.DomainRecord{
			{ID: 40, Name: "distributed systems"},
		},
		Methodologies: []jobs.MethodologyRecord{
			{ID: 50, Name: "TDD", Description: "Test-driven development"},
		},
		Projects: []jobs.ProjectRecord{
			{ID: 60, PersonID: 1, Name: "go-job", Description: "Job tracking tool", URL: "https://github.com/example/go-job"},
		},
		Educations: []jobs.EducationRecord{
			{ID: 70, PersonID: 1, School: "MIT", Degree: "B.S.", Field: "Computer Science", StartDate: "2010-09", EndDate: "2014-06"},
		},
		Certifications: []jobs.CertificationRecord{
			{ID: 80, PersonID: 1, Name: "CKA", Issuer: "CNCF", Year: "2022", URL: "https://cncf.io/cert/cka"},
		},
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, d); err != nil {
		t.Fatalf("template.Execute failed: %v", err)
	}
	out := buf.String()

	// Assert representative fields from each record type appear in rendered output.
	// A field-name typo in any {{range}} block causes the field to render as empty
	// string, failing the substring check below.
	checks := []struct {
		section string
		want    string
	}{
		{"person.Name", "Alice Engineer"},
		{"person.Email", "alice@example.com"},
		{"person.Summary", "Senior software engineer specializing in distributed systems."},
		{"experience.Title", "Staff Engineer"},
		{"experience.Company", "Acme Corp"},
		{"skill.Name", "Go"},
		{"skill.Level", "expert"},
		{"achievement.Text", "Reduced p99 latency by 40%"},
		{"achievement.Metric", "latency"},
		{"domain.Name", "distributed systems"},
		{"methodology.Name", "TDD"},
		{"project.Name", "go-job"},
		{"project.Description", "Job tracking tool"},
		{"education.School", "MIT"},
		{"education.Degree", "B.S."},
		{"certification.Name", "CKA"},
		{"certification.Issuer", "CNCF"},
		{"certification.Year", "2022"},
	}
	for _, c := range checks {
		if !strings.Contains(out, c.want) {
			t.Errorf("section %s: rendered output missing %q — possible field-name typo in template {{range}} block", c.section, c.want)
		}
	}
}

// TestResumePersonEditHandler_BadHourlyRate asserts that a non-empty, non-numeric
// hourly_rate returns HTTP 400 rather than silently writing 0.
// Red-on-revert: remove the parseErr != nil check → returns 303 (redirect) and writes 0.
func TestResumePersonEditHandler_BadHourlyRate(t *testing.T) {
	key := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	a := buildTestAuth(key)
	handler := resumePersonEditHandler(a, key)

	tok := csrf.Issue(key, "", csrf.DefaultTTL)
	form := url.Values{}
	form.Set(csrf.FormField, tok)
	form.Set("name", "Alice")        // required field
	form.Set("hourly_rate", "not-a-number")

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
		"/admin/resume/person", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rr := httptest.NewRecorder()
	handler(rr, req)

	// CSRF OK, name present, but hourly_rate is garbage → must 400 before any DB call.
	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400 for invalid hourly_rate, got %d: %s", rr.Code, rr.Body.String())
	}
}
