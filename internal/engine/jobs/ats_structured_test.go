package jobs

// ats_structured_test.go guards the structured JobListing mappers for the three
// ATS adapters (greenhouse, lever, ashby) and the source-structured-over-LLM
// precedence merge in job_search.
//
// Every assertion is an EXACT value (anti-vacuous): each test MUST go RED when
// the production mapping it guards is reverted. The mutation probe per test is
// documented in its comment.

import (
	"testing"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// --- Lever ---

// TestLeverPostingToListing_Salary verifies a Lever posting with
// salaryRange{min,max,currency} yields SalaryMin/SalaryMax/SalaryCurrency with
// the EXACT numbers, plus the human-readable Salary string.
//
// Mutation: drop the salaryRange mapping in leverPostingToListing → SalaryMin
// and SalaryMax stay nil, SalaryCurrency "" → RED.
func TestLeverPostingToListing_Salary(t *testing.T) {
	p := leverPosting{
		ID:        "abc123",
		Text:      "Golang Engineer",
		HostedURL: "https://jobs.lever.co/testco/abc123",
		Categories: struct {
			Location     string   `json:"location"`
			AllLocations []string `json:"allLocations"`
			Team         string   `json:"team"`
			Commitment   string   `json:"commitment"`
			Department   string   `json:"department"`
		}{Location: "Remote", Commitment: "Full-time"},
		WorkplaceType: "remote",
		CreatedAt:     1700000000000, // epoch ms
	}
	p.SalaryRange.Min = 160000
	p.SalaryRange.Max = 220000
	p.SalaryRange.Currency = "USD"

	const slug = "testco"
	const jobURL = "https://jobs.lever.co/testco/abc123"
	l := leverPostingToListing(p, slug, jobURL, "Remote")

	if l.SalaryMin == nil {
		t.Fatal("SalaryMin nil — salaryRange mapping dropped?")
	}
	if *l.SalaryMin != 160000 {
		t.Errorf("SalaryMin = %d, want 160000", *l.SalaryMin)
	}
	if l.SalaryMax == nil || *l.SalaryMax != 220000 {
		t.Errorf("SalaryMax = %v, want 220000", l.SalaryMax)
	}
	if l.SalaryCurrency != "USD" {
		t.Errorf("SalaryCurrency = %q, want %q", l.SalaryCurrency, "USD")
	}
	if l.Salary != "$160000-$220000 USD" {
		t.Errorf("Salary = %q, want %q", l.Salary, "$160000-$220000 USD")
	}
	// Adjacent structured fields must also be populated from the API.
	if l.Title != "Golang Engineer" {
		t.Errorf("Title = %q, want from API text", l.Title)
	}
	if l.JobType != "Full-time" {
		t.Errorf("JobType = %q, want %q (categories.commitment)", l.JobType, "Full-time")
	}
	if l.Remote != "remote" {
		t.Errorf("Remote = %q, want %q (workplaceType)", l.Remote, "remote")
	}
	if l.Company != slug {
		t.Errorf("Company = %q, want slug %q", l.Company, slug)
	}
	if l.URL != jobURL {
		t.Errorf("URL = %q, want %q", l.URL, jobURL)
	}
	if l.Source != "lever" {
		t.Errorf("Source = %q, want %q", l.Source, "lever")
	}
	if l.JobID != "abc123" {
		t.Errorf("JobID = %q, want %q", l.JobID, "abc123")
	}
	if l.Posted != "2023-11-14T22:13:20Z" {
		t.Errorf("Posted = %q, want ISO from createdAt epoch ms", l.Posted)
	}
}

// TestLeverPostingToListing_NoSalary verifies a Lever posting WITHOUT a salary
// range leaves the numeric salary pointers nil (no fabricated zeros).
func TestLeverPostingToListing_NoSalary(t *testing.T) {
	p := leverPosting{ID: "x", Text: "Eng", HostedURL: "https://jobs.lever.co/c/x"}
	l := leverPostingToListing(p, "c", "https://jobs.lever.co/c/x", "NYC")
	if l.SalaryMin != nil || l.SalaryMax != nil {
		t.Errorf("salary pointers = (%v,%v), want (nil,nil) when salaryRange absent", l.SalaryMin, l.SalaryMax)
	}
	if l.Salary != "" {
		t.Errorf("Salary = %q, want empty when salaryRange absent", l.Salary)
	}
}

// --- Ashby ---

// TestAshbyJobToListing_Remote verifies an Ashby posting with isRemote=true
// yields Remote="remote" and that buildAshbyLocation folds Remote into the
// location string (reused helper).
//
// Mutation: drop the isRemote→Remote mapping → Remote stays "" → RED.
func TestAshbyJobToListing_Remote(t *testing.T) {
	j := ashbyJob{
		ID:          "ash-1",
		Title:       "Staff Engineer",
		Location:    "San Francisco",
		IsRemote:    true,
		JobURL:      "https://jobs.ashbyhq.com/testco/ash-1",
		PublishedAt: "2026-03-01T00:00:00Z",
	}
	j.Compensation.CompensationTierSummary = "$200k-$280k USD"

	const slug = "testco"
	const jobURL = "https://jobs.ashbyhq.com/testco/ash-1"
	l := ashbyJobToListing(j, slug, jobURL)

	if l.Remote != "remote" {
		t.Errorf("Remote = %q, want %q (isRemote=true)", l.Remote, "remote")
	}
	// buildAshbyLocation reuse: location must carry " | Remote".
	if l.Location != "San Francisco | Remote" {
		t.Errorf("Location = %q, want %q (buildAshbyLocation reuse)", l.Location, "San Francisco | Remote")
	}
	if l.Salary != "$200k-$280k USD" {
		t.Errorf("Salary = %q, want compensationTierSummary verbatim", l.Salary)
	}
	if l.Title != "Staff Engineer" {
		t.Errorf("Title = %q, want from API", l.Title)
	}
	if l.Company != slug || l.URL != jobURL || l.Source != "ashby" {
		t.Errorf("Company/URL/Source = %q/%q/%q", l.Company, l.URL, l.Source)
	}
	if l.JobID != "ash-1" {
		t.Errorf("JobID = %q, want %q", l.JobID, "ash-1")
	}
	if l.Posted != "2026-03-01T00:00:00Z" {
		t.Errorf("Posted = %q, want publishedAt verbatim", l.Posted)
	}
}

// TestAshbyJobToListing_NotRemote_WorkplaceType verifies that when isRemote is
// false but workplaceType is set, Remote falls back to workplaceType.
func TestAshbyJobToListing_NotRemote_WorkplaceType(t *testing.T) {
	j := ashbyJob{ID: "x", Title: "Eng", Location: "Berlin", WorkplaceType: "hybrid", JobURL: "https://jobs.ashbyhq.com/c/x"}
	l := ashbyJobToListing(j, "c", "https://jobs.ashbyhq.com/c/x")
	if l.Remote != "hybrid" {
		t.Errorf("Remote = %q, want %q (workplaceType fallback)", l.Remote, "hybrid")
	}
}

// --- Greenhouse ---

// TestGreenhouseJobToListing verifies a Greenhouse posting yields
// Title/Company/Location/URL/Posted/JobID from the parsed API struct, NOT from
// any parsed markdown string.
//
// Mutation: drop the mapping (return zero JobListing) → every field empty → RED.
func TestGreenhouseJobToListing(t *testing.T) {
	job := greenhouseJob{
		ID:          123456,
		Title:       "Senior Go Engineer",
		UpdatedAt:   "2026-02-10T12:00:00Z",
		AbsoluteURL: "https://boards.greenhouse.io/stripe/jobs/123456",
	}
	job.Location.Name = "Remote"
	job.Departments = []struct {
		Name string `json:"name"`
	}{{Name: "Engineering"}}

	const slug = "stripe"
	const jobURL = "https://boards.greenhouse.io/stripe/jobs/123456"
	l := greenhouseJobToListing(job, slug, jobURL)

	if l.Title != "Senior Go Engineer" {
		t.Errorf("Title = %q, want from API title field", l.Title)
	}
	if l.Company != "stripe" {
		t.Errorf("Company = %q, want board slug", l.Company)
	}
	if l.Location != "Remote" {
		t.Errorf("Location = %q, want location.name", l.Location)
	}
	if l.URL != jobURL {
		t.Errorf("URL = %q, want absolute_url", l.URL)
	}
	if l.Posted != "2026-02-10T12:00:00Z" {
		t.Errorf("Posted = %q, want updated_at verbatim", l.Posted)
	}
	if l.JobID != "123456" {
		t.Errorf("JobID = %q, want %q", l.JobID, "123456")
	}
	if l.Source != "greenhouse" {
		t.Errorf("Source = %q, want %q", l.Source, "greenhouse")
	}
}

// --- Precedence (job_search source-structured over LLM) ---

// TestApplyStructuredPrecedence_SalaryWins verifies that given a structured
// listing with SalaryMin=160000 and an LLM record for the same URL with an
// empty SalaryMin + Salary "not specified", the output carries 160000 and the
// structured salary string.
//
// Mutation: reverse the precedence (LLM wins) → SalaryMin stays nil, Salary
// stays "not specified" → RED.
func TestApplyStructuredPrecedence_SalaryWins(t *testing.T) {
	min := 160000
	max := 220000
	structured := map[string]engine.JobListing{
		"https://jobs.lever.co/testco/abc": {
			URL:            "https://jobs.lever.co/testco/abc",
			SalaryMin:      &min,
			SalaryMax:      &max,
			SalaryCurrency: "USD",
			Salary:         "$160000-$220000 USD",
			Company:        "testco",
			Location:       "Remote",
		},
	}
	llm := []engine.JobListing{
		{
			URL:       "https://jobs.lever.co/testco/abc",
			Title:     "Golang Engineer", // LLM-only field, structured empty → kept
			Salary:    "not specified",   // LLM produced this; structured overrides
			SalaryMin: nil,
			Company:   "", // LLM empty → structured fills
		},
	}

	ApplyStructuredPrecedence(llm, structured)

	if llm[0].SalaryMin == nil || *llm[0].SalaryMin != 160000 {
		t.Errorf("SalaryMin = %v, want 160000 (structured wins over LLM nil)", llm[0].SalaryMin)
	}
	if llm[0].SalaryMax == nil || *llm[0].SalaryMax != 220000 {
		t.Errorf("SalaryMax = %v, want 220000", llm[0].SalaryMax)
	}
	if llm[0].SalaryCurrency != "USD" {
		t.Errorf("SalaryCurrency = %q, want %q", llm[0].SalaryCurrency, "USD")
	}
	if llm[0].Salary != "$160000-$220000 USD" {
		t.Errorf("Salary = %q, want structured string (not %q)", llm[0].Salary, "not specified")
	}
	// LLM-only field preserved where structured is empty.
	if llm[0].Title != "Golang Engineer" {
		t.Errorf("Title = %q, want LLM value preserved", llm[0].Title)
	}
	// Structured fills LLM gap.
	if llm[0].Company != "testco" {
		t.Errorf("Company = %q, want structured fill", llm[0].Company)
	}
	if llm[0].Location != "Remote" {
		t.Errorf("Location = %q, want structured fill", llm[0].Location)
	}
}

// TestApplyStructuredPrecedence_NoStructured verifies an LLM record with no
// matching structured listing is left untouched (the generic-searxng /
// LinkedIn path).
func TestApplyStructuredPrecedence_NoStructured(t *testing.T) {
	llm := []engine.JobListing{
		{URL: "https://example.com/other", Title: "Other", Salary: "not specified"},
	}
	ApplyStructuredPrecedence(llm, map[string]engine.JobListing{})
	if llm[0].Title != "Other" {
		t.Errorf("Title = %q, want untouched", llm[0].Title)
	}
	if llm[0].Salary != "not specified" {
		t.Errorf("Salary = %q, want untouched (no structured match)", llm[0].Salary)
	}
}
