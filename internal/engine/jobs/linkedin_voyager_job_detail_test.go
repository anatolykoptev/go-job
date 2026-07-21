package jobs

import (
	"testing"

	linkedin "github.com/anatolykoptev/go-linkedin"
)

// TestJobDetailToFieldsEasyApply verifies the mapper populates the go-job-side
// enrichment fields from a linkedin.JobDetail, including the ApplyMethod
// derivation: EasyApply=true -> "easy-apply", EasyApply=false -> "off-site".
func TestJobDetailToFieldsEasyApply(t *testing.T) {
	d := &linkedin.JobDetail{
		JobID:          "4335742219",
		Title:          "Senior Go Engineer",
		Company:        "Acme",
		ApplicantCount: 137,
		SeniorityLevel: "Mid-Senior",
		JobFunction:    "Engineering",
		EmploymentType: "FULL_TIME",
		EasyApply:      true,
		HiringTeam: []linkedin.HiringTeamMember{
			{Name: "Jane Doe", Title: "Tech Recruiter", ProfileURL: "https://www.linkedin.com/in/janedoe"},
			{Name: "John Smith", Title: "Hiring Manager"},
		},
	}

	f := jobDetailToFields(d)

	if !f.EasyApply {
		t.Errorf("EasyApply = false, want true")
	}
	if f.ApplyMethod != "easy-apply" {
		t.Errorf("ApplyMethod = %q, want %q", f.ApplyMethod, "easy-apply")
	}
	if f.ApplicantCount != 137 {
		t.Errorf("ApplicantCount = %d, want 137", f.ApplicantCount)
	}
	if f.SeniorityLevel != "Mid-Senior" {
		t.Errorf("SeniorityLevel = %q, want %q", f.SeniorityLevel, "Mid-Senior")
	}
	if f.JobFunction != "Engineering" {
		t.Errorf("JobFunction = %q, want %q", f.JobFunction, "Engineering")
	}
	if f.EmploymentType != "FULL_TIME" {
		t.Errorf("EmploymentType = %q, want %q", f.EmploymentType, "FULL_TIME")
	}
	if len(f.HiringTeam) != 2 {
		t.Fatalf("HiringTeam len = %d, want 2", len(f.HiringTeam))
	}
	wantFirst := HiringTeamMember{Name: "Jane Doe", Title: "Tech Recruiter", ProfileURL: "https://www.linkedin.com/in/janedoe"}
	if f.HiringTeam[0] != wantFirst {
		t.Errorf("HiringTeam[0] = %+v, want %+v", f.HiringTeam[0], wantFirst)
	}
	wantSecond := HiringTeamMember{Name: "John Smith", Title: "Hiring Manager"}
	if f.HiringTeam[1] != wantSecond {
		t.Errorf("HiringTeam[1] = %+v, want %+v", f.HiringTeam[1], wantSecond)
	}
}

// TestJobDetailToFieldsOffSite verifies ApplyMethod derivation for a non-easy-apply posting.
func TestJobDetailToFieldsOffSite(t *testing.T) {
	d := &linkedin.JobDetail{
		JobID:     "4335742220",
		Title:     "Staff Engineer",
		Company:   "Globex",
		EasyApply: false,
	}

	f := jobDetailToFields(d)

	if f.EasyApply {
		t.Errorf("EasyApply = true, want false")
	}
	if f.ApplyMethod != "off-site" {
		t.Errorf("ApplyMethod = %q, want %q", f.ApplyMethod, "off-site")
	}
	if f.ApplicantCount != 0 {
		t.Errorf("ApplicantCount = %d, want 0", f.ApplicantCount)
	}
	if len(f.HiringTeam) != 0 {
		t.Errorf("HiringTeam len = %d, want 0", len(f.HiringTeam))
	}
}

// TestJobDetailToFieldsNil is the nil-safety guard: a nil JobDetail must not panic.
func TestJobDetailToFieldsNil(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("jobDetailToFields(nil) panicked: %v", r)
		}
	}()
	f := jobDetailToFields(nil)
	if f.EasyApply || f.ApplyMethod != "" || f.ApplicantCount != 0 {
		t.Errorf("jobDetailToFields(nil) = %+v, want zero value", f)
	}
}

// TestJobDetailToFieldsRoundTripsOntoLinkedInJob verifies the mapper output can
// be assigned onto a LinkedInJob and the enrichment fields survive JSON
// round-trip with omitempty semantics.
func TestJobDetailToFieldsRoundTripsOntoLinkedInJob(t *testing.T) {
	d := &linkedin.JobDetail{
		JobID:          "4335742221",
		Title:          "Backend Engineer",
		Company:        "Initech",
		ApplicantCount: 42,
		SeniorityLevel: "Entry",
		EasyApply:      true,
		HiringTeam:     []linkedin.HiringTeamMember{{Name: "Recruiter"}},
	}
	f := jobDetailToFields(d)

	job := LinkedInJob{
		JobID:           d.JobID,
		Title:           d.Title,
		Company:         d.Company,
		EasyApply:       f.EasyApply,
		ApplyMethod:     f.ApplyMethod,
		CompanyApplyURL: f.CompanyApplyURL,
		ApplicantCount:  f.ApplicantCount,
		SeniorityLevel:  f.SeniorityLevel,
		JobFunction:     f.JobFunction,
		EmploymentType:  f.EmploymentType,
		HiringTeam:      f.HiringTeam,
	}

	if job.ApplyMethod != "easy-apply" {
		t.Errorf("ApplyMethod = %q, want easy-apply", job.ApplyMethod)
	}
	if job.ApplicantCount != 42 {
		t.Errorf("ApplicantCount = %d, want 42", job.ApplicantCount)
	}
	if len(job.HiringTeam) != 1 || job.HiringTeam[0].Name != "Recruiter" {
		t.Errorf("HiringTeam = %+v, want [{Name:Recruiter}]", job.HiringTeam)
	}
}
