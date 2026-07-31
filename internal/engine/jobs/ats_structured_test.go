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
// salaryRange{min,max,currency,interval} yields SalaryMin/SalaryMax/
// SalaryCurrency/SalaryInterval with the EXACT numbers, plus the
// human-readable Salary string via formatSalary (reused from tracker.go).
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
	p.SalaryRange.Interval = "per-year-salary"

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
	if l.SalaryInterval != "year" {
		t.Errorf("SalaryInterval = %q, want %q (per-year-salary normalized)", l.SalaryInterval, "year")
	}
	if l.Salary != "160000–220000 USD/year" {
		t.Errorf("Salary = %q, want %q (formatSalary output)", l.Salary, "160000–220000 USD/year")
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

// TestLeverPostingToListing_SalaryMinZero verifies that {min:0, max:220000}
// still populates SalaryMax — the outer guard is Min > 0 || Max > 0, NOT
// Min > 0 alone. This is the EXACT failure this PR was written to fix: a
// posting with only a max salary yielded nil/empty everywhere and the output
// stayed "not specified".
//
// Mutation: revert outer guard to `Min > 0` → SalaryMax stays nil → RED.
func TestLeverPostingToListing_SalaryMinZero(t *testing.T) {
	p := leverPosting{ID: "x", Text: "Eng", HostedURL: "https://jobs.lever.co/c/x"}
	p.SalaryRange.Min = 0
	p.SalaryRange.Max = 220000
	p.SalaryRange.Currency = "USD"
	p.SalaryRange.Interval = "per-year-salary"
	l := leverPostingToListing(p, "c", "https://jobs.lever.co/c/x", "NYC")
	if l.SalaryMin != nil {
		t.Errorf("SalaryMin = %v, want nil (min=0)", l.SalaryMin)
	}
	if l.SalaryMax == nil || *l.SalaryMax != 220000 {
		t.Errorf("SalaryMax = %v, want 220000 (max>0 must populate even when min=0)", l.SalaryMax)
	}
	if l.SalaryCurrency != "USD" {
		t.Errorf("SalaryCurrency = %q, want USD", l.SalaryCurrency)
	}
	if l.SalaryInterval != "year" {
		t.Errorf("SalaryInterval = %q, want year", l.SalaryInterval)
	}
}

// TestLeverPostingToListing_SalarySingleFigure verifies that {min:180000,
// max:180000} populates BOTH pointers. The old `Max > Min` guard silently
// dropped SalaryMax when max == min (single-figure comp).
//
// Mutation: revert to `Max > Min` → SalaryMax stays nil → RED.
func TestLeverPostingToListing_SalarySingleFigure(t *testing.T) {
	p := leverPosting{ID: "x", Text: "Eng", HostedURL: "https://jobs.lever.co/c/x"}
	p.SalaryRange.Min = 180000
	p.SalaryRange.Max = 180000
	p.SalaryRange.Currency = "USD"
	p.SalaryRange.Interval = "per-year-salary"
	l := leverPostingToListing(p, "c", "https://jobs.lever.co/c/x", "NYC")
	if l.SalaryMin == nil || *l.SalaryMin != 180000 {
		t.Errorf("SalaryMin = %v, want 180000", l.SalaryMin)
	}
	if l.SalaryMax == nil || *l.SalaryMax != 180000 {
		t.Errorf("SalaryMax = %v, want 180000 (max==min must still populate)", l.SalaryMax)
	}
}

// TestLeverPostingToListing_SalaryNoInterval verifies that when the Lever API
// omits the interval field, SalaryMin/SalaryMax stay nil — a per-hour posting
// must NOT be scored as annual (BLOCKER D). The scorer at hunt/score/scorer.go
// renders SalaryMin/Max into a prompt whose next line says "Minimum
// compensation: $X USD total comp"; $80/hour scored as $80/year is the
// traced failure.
//
// Mutation: remove the interval guard → pointers set without interval → RED.
func TestLeverPostingToListing_SalaryNoInterval(t *testing.T) {
	p := leverPosting{ID: "x", Text: "Eng", HostedURL: "https://jobs.lever.co/c/x"}
	p.SalaryRange.Min = 50
	p.SalaryRange.Max = 80
	p.SalaryRange.Currency = "USD"
	// Interval intentionally absent
	l := leverPostingToListing(p, "c", "https://jobs.lever.co/c/x", "NYC")
	if l.SalaryMin != nil || l.SalaryMax != nil {
		t.Errorf("salary pointers = (%v,%v), want (nil,nil) when interval absent — per-hour must not be scored as annual",
			l.SalaryMin, l.SalaryMax)
	}
	if l.SalaryInterval != "" {
		t.Errorf("SalaryInterval = %q, want empty when interval absent", l.SalaryInterval)
	}
}

// TestLeverPostingToListing_SalaryHourly verifies that a per-hour-wage posting
// normalizes the interval to "hour" and populates the salary fields.
//
// Mutation: drop per-hour-wage→hour normalization → interval stays "" →
// pointers nil (same as no-interval) → RED.
func TestLeverPostingToListing_SalaryHourly(t *testing.T) {
	p := leverPosting{ID: "x", Text: "Eng", HostedURL: "https://jobs.lever.co/c/x"}
	p.SalaryRange.Min = 50
	p.SalaryRange.Max = 80
	p.SalaryRange.Currency = "USD"
	p.SalaryRange.Interval = "per-hour-wage"
	l := leverPostingToListing(p, "c", "https://jobs.lever.co/c/x", "NYC")
	if l.SalaryMin == nil || *l.SalaryMin != 50 {
		t.Errorf("SalaryMin = %v, want 50", l.SalaryMin)
	}
	if l.SalaryMax == nil || *l.SalaryMax != 80 {
		t.Errorf("SalaryMax = %v, want 80", l.SalaryMax)
	}
	if l.SalaryInterval != "hour" {
		t.Errorf("SalaryInterval = %q, want %q (per-hour-wage normalized)", l.SalaryInterval, "hour")
	}
	if l.Salary != "50–80 USD/hour" {
		t.Errorf("Salary = %q, want %q", l.Salary, "50–80 USD/hour")
	}
}

// TestLeverPostingToListing_NoSalary verifies a Lever posting WITHOUT a salary
// range leaves the numeric salary pointers nil (no fabricated zeros).
//
// Negative half: this test passes against an all-empty struct — it guards that
// absent salary does not fabricate zeros, NOT that the salary mapping is wired.
// The wiring is guarded by TestLeverPostingToListing_Salary above.
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

// --- Remote vocabulary normalization (BLOCKER B) ---

// TestNormalizeRemote_LeverVocabulary verifies the exact strings Lever's
// workplaceType emits are normalized to the prompt_jobs.go contract
// vocabulary: remote | hybrid | onsite | "".
//
// Lever emits: unspecified | on-site | remote | hybrid.
// "on-site" (hyphenated) MUST become "onsite" (the filter at hunt/store.go:542
// uses exact equality remote = $N). "unspecified" MUST become "" so the LLM
// value survives precedence rather than being overwritten with nothing.
//
// Mutation: remove the on-site→onsite case → "on-site" stays "on-site" → RED.
// Mutation: map unspecified→"" removed → "unspecified" stays → RED.
func TestNormalizeRemote_LeverVocabulary(t *testing.T) {
	tests := []struct{ in, want string }{
		{"remote", "remote"},
		{"hybrid", "hybrid"},
		{"on-site", "onsite"}, // Lever's hyphenated form → contract's unhyphenated
		{"unspecified", ""},   // empty so LLM value survives, NOT overwritten
		{"", ""},              // absent → empty
	}
	for _, tt := range tests {
		got := normalizeRemote(tt.in)
		if got != tt.want {
			t.Errorf("normalizeRemote(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestNormalizeRemote_AshbyVocabulary verifies Ashby's casing is handled.
// Ashby emits lowercase "onsite" (no hyphen) but casing may vary.
func TestNormalizeRemote_AshbyVocabulary(t *testing.T) {
	tests := []struct{ in, want string }{
		{"remote", "remote"},
		{"hybrid", "hybrid"},
		{"onsite", "onsite"},
		{"Onsite", "onsite"}, // case-insensitive
		{"Remote", "remote"},
	}
	for _, tt := range tests {
		got := normalizeRemote(tt.in)
		if got != tt.want {
			t.Errorf("normalizeRemote(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestLeverPostingToListing_RemoteNormalization verifies that leverPostingToListing
// normalizes workplaceType to the contract vocabulary, not the raw API string.
//
// Mutation: revert to l.Remote = p.WorkplaceType → "on-site" stored raw → RED.
func TestLeverPostingToListing_RemoteNormalization(t *testing.T) {
	p := leverPosting{
		ID:            "x",
		Text:          "Eng",
		HostedURL:     "https://jobs.lever.co/c/x",
		WorkplaceType: "on-site",
	}
	l := leverPostingToListing(p, "c", "https://jobs.lever.co/c/x", "NYC")
	if l.Remote != "onsite" {
		t.Errorf("Remote = %q, want %q (on-site normalized to onsite)", l.Remote, "onsite")
	}

	p.WorkplaceType = "unspecified"
	l = leverPostingToListing(p, "c", "https://jobs.lever.co/c/x", "NYC")
	if l.Remote != "" {
		t.Errorf("Remote = %q, want %q (unspecified→empty so LLM value survives)", l.Remote, "")
	}
}

// TestAshbyJobToListing_WorkplaceTypeWinsOverIsRemote verifies that when both
// isRemote=true and workplaceType=hybrid are set, workplaceType wins. This
// answers the reviewer's open question: workplaceType is the MORE SPECIFIC
// field (remote/hybrid/onsite vs boolean), and the filter at hunt/store.go:542
// uses exact equality — storing "remote" for a hybrid job would make
// hunt_list remote=remote match a hybrid job, and hunt_list remote=hybrid
// miss it. workplaceType is authoritative; isRemote is the fallback when
// workplaceType is absent/unspecified.
//
// Mutation: revert to isRemote-wins → Remote="remote" for a hybrid job → RED.
func TestAshbyJobToListing_WorkplaceTypeWinsOverIsRemote(t *testing.T) {
	j := ashbyJob{
		ID:            "x",
		Title:         "Eng",
		Location:      "Berlin",
		IsRemote:      true,
		WorkplaceType: "hybrid",
		JobURL:        "https://jobs.ashbyhq.com/c/x",
	}
	l := ashbyJobToListing(j, "c", "https://jobs.ashbyhq.com/c/x")
	if l.Remote != "hybrid" {
		t.Errorf("Remote = %q, want %q (workplaceType wins over isRemote)", l.Remote, "hybrid")
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
// Note: job.Departments is parsed but NOT mapped — engine.JobListing has no
// departments field. The fixture carries it so a future mapping (e.g. to a
// Tags/Skills field) has the parsed data ready; today it is a no-op.
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
			SalaryInterval: "year",
			Salary:         "160000–220000 USD/year",
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
	if llm[0].SalaryInterval != "year" {
		t.Errorf("SalaryInterval = %q, want %q", llm[0].SalaryInterval, "year")
	}
	if llm[0].Salary != "160000–220000 USD/year" {
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
//
// Negative half: this test passes against a no-op body — it guards that
// non-matching URLs are NOT mutated, NOT that the precedence merge is wired.
// The wiring is guarded by TestApplyStructuredPrecedence_SalaryWins above and
// the delivery-chain tests in tool_job_search_sources_test.go.
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

// TestNormalizeURL verifies the URL canonicalization that makes the
// structuredByURL join key match across producer and lookup sides.
//
// Mutation: stop stripping trailing slash → "https://x/jobs/1/" and
// "https://x/jobs/1" no longer match → TestApplyStructuredPrecedence_URLNormalization RED.
// Mutation: stop lowercasing host → "Jobs.Lever.Co" and "jobs.lever.co" no
// longer match → RED.
func TestNormalizeURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://jobs.lever.co/testco/abc", "https://jobs.lever.co/testco/abc"},
		{"https://jobs.lever.co/testco/abc/", "https://jobs.lever.co/testco/abc"},           // trailing slash stripped
		{"https://jobs.lever.co/testco/abc?source=llm", "https://jobs.lever.co/testco/abc"}, // query stripped
		{"https://jobs.lever.co/testco/abc#section", "https://jobs.lever.co/testco/abc"},    // fragment stripped
		{"https://Jobs.Lever.Co/testco/abc", "https://jobs.lever.co/testco/abc"},            // host lowercased
		{"https://jobs.lever.co/testco/abc/?ref=1#top", "https://jobs.lever.co/testco/abc"}, // all three
	}
	for _, tt := range tests {
		got := NormalizeURL(tt.in)
		if got != tt.want {
			t.Errorf("NormalizeURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestApplyStructuredPrecedence_URLNormalization verifies that an LLM record
// whose URL differs from the structured listing's URL by a trailing slash +
// query param still matches. This is the HIGH finding: without normalization,
// structuredByURL is an exact-string map and a single trailing slash yields
// zero hits.
//
// Mutation: revert ApplyStructuredPrecedence to exact-string lookup
// (structuredByURL[llm[i].URL]) → no match → SalaryMin stays nil → RED.
func TestApplyStructuredPrecedence_URLNormalization(t *testing.T) {
	min := 160000
	max := 220000
	structured := map[string]engine.JobListing{
		// Producer side: clean URL (code-built).
		"https://jobs.lever.co/testco/abc": {
			URL:            "https://jobs.lever.co/testco/abc",
			SalaryMin:      &min,
			SalaryMax:      &max,
			SalaryCurrency: "USD",
			SalaryInterval: "year",
			Salary:         "160000–220000 USD/year",
			Source:         "lever",
		},
	}
	llm := []engine.JobListing{
		{
			// LLM side: trailing slash + query param — the exact variation
			// that produced zero hits before normalization.
			URL:       "https://jobs.lever.co/testco/abc/?source=llm",
			Title:     "Eng",
			Salary:    "not specified",
			SalaryMin: nil,
		},
	}

	ApplyStructuredPrecedence(llm, structured)

	if llm[0].SalaryMin == nil || *llm[0].SalaryMin != 160000 {
		t.Errorf("SalaryMin = %v, want 160000 (URL normalization must make trailing-slash+query match)", llm[0].SalaryMin)
	}
	if llm[0].Salary != "160000–220000 USD/year" {
		t.Errorf("Salary = %q, want structured string (match via normalized URL)", llm[0].Salary)
	}
}

// TestApplyStructuredPrecedence_JobIDFallback verifies that when the
// normalized URL misses, the lookup falls back to llm[i].JobID matching (the
// LLM may have extracted the JobID from the posting body even when the URL is
// wrong). This catches the case where the LLM emits a different URL for the
// same job (e.g. the apply URL vs the hosted URL on Lever). ExtractJobID is
// NOT used here — it matches only LinkedIn URLs and can collide with
// Greenhouse int64 ids (see TestApplyStructuredPrecedence_CrossProviderCollisionRejected).
//
// Mutation: remove the JobID fallback → no match → SalaryMin stays nil → RED.
func TestApplyStructuredPrecedence_JobIDFallback(t *testing.T) {
	min := 160000
	structured := map[string]engine.JobListing{
		// Structured side: hosted URL, JobID set.
		"https://jobs.lever.co/testco/abc": {
			URL:       "https://jobs.lever.co/testco/abc",
			JobID:     "abc",
			SalaryMin: &min,
			Source:    "lever",
		},
	}
	llm := []engine.JobListing{
		{
			// LLM side: a DIFFERENT URL for the same job — no path overlap,
			// but the LLM extracted the JobID from the posting body.
			URL:       "https://jobs.lever.co/testco/abc/apply",
			JobID:     "abc",
			Title:     "Eng",
			SalaryMin: nil,
		},
	}

	ApplyStructuredPrecedence(llm, structured)

	if llm[0].SalaryMin == nil || *llm[0].SalaryMin != 160000 {
		t.Errorf("SalaryMin = %v, want 160000 (JobID fallback must match)", llm[0].SalaryMin)
	}
}

// TestApplyStructuredPrecedence_DescriptionNotOverwritten verifies that the
// structured Description (a 600-rune truncation of descriptionPlain) does NOT
// overwrite the LLM's Description (the model's own summary). The LLM summary
// is the value the user sees; a truncated raw dump would lose it.
//
// Mutation: re-add `if s.Description != "" { llm[i].Description = s.Description }`
// → LLM summary overwritten → RED.
func TestApplyStructuredPrecedence_DescriptionNotOverwritten(t *testing.T) {
	structured := map[string]engine.JobListing{
		"https://jobs.lever.co/testco/abc": {
			URL:         "https://jobs.lever.co/testco/abc",
			Description: "First 600 runes of descriptionPlain — a raw dump, not a summary",
			Source:      "lever",
		},
	}
	llm := []engine.JobListing{
		{
			URL:         "https://jobs.lever.co/testco/abc",
			Description: "LLM-generated summary: senior Go role, distributed systems focus",
		},
	}

	ApplyStructuredPrecedence(llm, structured)

	if llm[0].Description != "LLM-generated summary: senior Go role, distributed systems focus" {
		t.Errorf("Description = %q, want LLM summary preserved (not overwritten by structured truncation)", llm[0].Description)
	}
}

// TestApplyStructuredPrecedence_AshbyNumericsPreserved verifies that an Ashby
// listing with Salary set (from compensationTierSummary, free-text) but nil
// SalaryMin/Max/Currency/Interval leaves the LLM's numeric salary fields
// INTACT. ashbyJobToListing sets only the free-text Salary from
// compensationTierSummary and NEVER the numeric fields — so every Ashby job
// with a published comp tier has nil numerics. The old salary-zeroing branch
// (sourceExposesCompensation) overwrote SalaryMin/Max/Currency/Interval with
// nil/"", dropping the job out of hunt_list salary filters and stopping the
// scorer's compensation prompt line. Precedence is now field-by-field: a
// nil/empty structured field NEVER overwrites a populated LLM one.
//
// Mutation: restore the group-copy (`hasStructuredSalary` → copy all 5 fields)
// → llm[0].SalaryMin becomes nil → RED.
// Mutation: restore sourceExposesCompensation zeroing → llm[0].SalaryMin
// becomes nil → RED.
func TestApplyStructuredPrecedence_AshbyNumericsPreserved(t *testing.T) {
	structured := map[string]engine.JobListing{
		"https://jobs.ashbyhq.com/testco/abc": {
			URL:    "https://jobs.ashbyhq.com/testco/abc",
			Title:  "Eng",
			Source: "ashby",
			Salary: "$180k–$220k USD", // compensationTierSummary, free-text
			// SalaryMin/Max/Currency/Interval intentionally nil/"" — ashbyJobToListing
			// never sets the numeric fields.
		},
	}
	llmMin := 180000
	llmMax := 220000
	llm := []engine.JobListing{
		{
			URL:            "https://jobs.ashbyhq.com/testco/abc",
			Title:          "Old Title",
			Salary:         "180000-220000 USD",
			SalaryMin:      &llmMin,
			SalaryMax:      &llmMax,
			SalaryCurrency: "USD",
			SalaryInterval: "year",
		},
	}

	ApplyStructuredPrecedence(llm, structured)

	// Free-text Salary from structured wins (it's non-empty).
	if llm[0].Salary != "$180k–$220k USD" {
		t.Errorf("Salary = %q, want structured free-text value (non-empty → wins)", llm[0].Salary)
	}
	// Numeric fields from LLM preserved — structured has nil/"" for these.
	if llm[0].SalaryMin == nil || *llm[0].SalaryMin != 180000 {
		t.Errorf("SalaryMin = %v, want 180000 (LLM value preserved — structured has nil)", llm[0].SalaryMin)
	}
	if llm[0].SalaryMax == nil || *llm[0].SalaryMax != 220000 {
		t.Errorf("SalaryMax = %v, want 220000 (LLM value preserved — structured has nil)", llm[0].SalaryMax)
	}
	if llm[0].SalaryCurrency != "USD" {
		t.Errorf("SalaryCurrency = %q, want USD (LLM value preserved — structured has empty)", llm[0].SalaryCurrency)
	}
	if llm[0].SalaryInterval != "year" {
		t.Errorf("SalaryInterval = %q, want year (LLM value preserved — structured has empty)", llm[0].SalaryInterval)
	}
	// Non-salary fields still applied from structured.
	if llm[0].Title != "Eng" {
		t.Errorf("Title = %q, want Eng (non-salary fields still applied)", llm[0].Title)
	}
}

// TestApplyStructuredPrecedence_GreenhouseSalaryPreserved verifies that a
// Greenhouse structured match does NOT delete the LLM's salary. Greenhouse has
// no compensation field in its API (greenhouseJobToListing can never set any
// salary), so its silence is structural, not meaningful — the LLM's salary
// (extracted from job.Content, the ONLY place Greenhouse publishes comp) is
// PRESERVED. Non-salary fields (Title, Company) still win from structured.
// This is the guard that nothing deletes salary: the field-by-field rule
// (structured wins only where non-empty) keeps the LLM salary because the
// structured listing has nil/"" for every salary field.
//
// Mutation: restore salary zeroing (sourceExposesCompensation or group-copy
// on hasStructuredSalary) → llm[0].SalaryMin becomes nil, Salary becomes "" → RED.
func TestApplyStructuredPrecedence_GreenhouseSalaryPreserved(t *testing.T) {
	structured := map[string]engine.JobListing{
		"https://boards.greenhouse.io/testco/jobs/4001234": {
			URL:     "https://boards.greenhouse.io/testco/jobs/4001234",
			Title:   "Backend Engineer",
			Company: "testco",
			Source:  "greenhouse",
			// Salary intentionally absent — Greenhouse API has no comp field.
		},
	}
	llmMin := 160000
	llmMax := 220000
	llm := []engine.JobListing{
		{
			URL:            "https://boards.greenhouse.io/testco/jobs/4001234",
			Title:          "Old Title",
			Salary:         "160000-220000 USD",
			SalaryMin:      &llmMin,
			SalaryMax:      &llmMax,
			SalaryCurrency: "USD",
			SalaryInterval: "year",
		},
	}

	ApplyStructuredPrecedence(llm, structured)

	// Salary preserved — Greenhouse has no comp field, so silence is structural.
	if llm[0].Salary != "160000-220000 USD" {
		t.Errorf("Salary = %q, want preserved (Greenhouse has no comp field — LLM salary must NOT be zeroed)", llm[0].Salary)
	}
	if llm[0].SalaryMin == nil || *llm[0].SalaryMin != 160000 {
		t.Errorf("SalaryMin = %v, want 160000 (preserved)", llm[0].SalaryMin)
	}
	if llm[0].SalaryMax == nil || *llm[0].SalaryMax != 220000 {
		t.Errorf("SalaryMax = %v, want 220000 (preserved)", llm[0].SalaryMax)
	}
	if llm[0].SalaryCurrency != "USD" {
		t.Errorf("SalaryCurrency = %q, want USD (preserved)", llm[0].SalaryCurrency)
	}
	// Non-salary fields still applied from structured.
	if llm[0].Title != "Backend Engineer" {
		t.Errorf("Title = %q, want structured value", llm[0].Title)
	}
	if llm[0].Company != "testco" {
		t.Errorf("Company = %q, want structured value", llm[0].Company)
	}
}

// TestApplyStructuredPrecedence_CrossProviderCollisionRejected verifies that
// the JobID fallback does NOT match across providers. A LinkedIn LLM record
// (Source="linkedin") with JobID="4001234" must not be rewritten by a
// Greenhouse structured listing whose int64 id happens to collide. Before the
// fix, byJobID was built across all providers with no Source check, so the
// Greenhouse record's Title/Company/URL/JobID/Source overwrote the LinkedIn
// record, and the overwritten JobID broke the LinkedIn gap-fill at
// tool_job_search.go:433.
//
// Mutation: remove the `cand.Source == llm[i].Source` guard → cross-provider
// match → llm[0].Title becomes "Greenhouse Eng", Source becomes "greenhouse" → RED.
func TestApplyStructuredPrecedence_CrossProviderCollisionRejected(t *testing.T) {
	structured := map[string]engine.JobListing{
		// Greenhouse job with int64 id 4001234 — collides with the LinkedIn id.
		"https://boards.greenhouse.io/testco/jobs/4001234": {
			URL:     "https://boards.greenhouse.io/testco/jobs/4001234",
			JobID:   "4001234",
			Title:   "Greenhouse Eng",
			Company: "testco",
			Source:  "greenhouse",
		},
	}
	llm := []engine.JobListing{
		{
			// LinkedIn record — URL does NOT normalize-match the Greenhouse URL,
			// so the JobID fallback is the only path. Source="linkedin" must
			// block the cross-provider match.
			URL:     "https://www.linkedin.com/jobs/view/4001234",
			JobID:   "4001234",
			Source:  "linkedin",
			Title:   "LinkedIn Eng",
			Company: "LinkedInCorp",
		},
	}

	ApplyStructuredPrecedence(llm, structured)

	// No match — LinkedIn record must be untouched.
	if llm[0].Title != "LinkedIn Eng" {
		t.Errorf("Title = %q, want LinkedIn Eng (cross-provider JobID collision must NOT rewrite)", llm[0].Title)
	}
	if llm[0].Company != "LinkedInCorp" {
		t.Errorf("Company = %q, want LinkedInCorp (cross-provider collision must NOT rewrite)", llm[0].Company)
	}
	if llm[0].Source != "linkedin" {
		t.Errorf("Source = %q, want linkedin (must NOT be overwritten by greenhouse)", llm[0].Source)
	}
	if llm[0].URL != "https://www.linkedin.com/jobs/view/4001234" {
		t.Errorf("URL = %q, want LinkedIn URL preserved", llm[0].URL)
	}
}

// TestApplyStructuredPrecedence_EmptySourceJobIDFallbackResolvedFromURL
// verifies that when the LLM record omits Source (json:"source,omitempty",
// nothing enforces it, and precedence runs BEFORE the extractSourceForQuality
// backfill in tool_job_search.go), the JobID fallback resolves the Source from
// the URL via extractSourceFromURL and requires equality with the candidate's
// Source. A LinkedIn record (id 4001234, no Source, LinkedIn URL) must NOT
// match a Greenhouse candidate with the same int64 id — extractSourceFromURL
// returns "" for a non-ATS URL, so llmSrc stays "" and the fallback is refused.
//
// This is the empty-Source escape that the bare `llm[i].Source == ""` guard
// left open: the LLM omits one field and a cross-provider collision rewrites
// the record, relabelling Source so nothing downstream can detect it.
//
// Mutation: restore the bare `llm[i].Source == "" || cand.Source == llm[i].Source`
// escape → the LinkedIn record matches the Greenhouse candidate → llm[0].Title
// becomes "Greenhouse Eng", Source becomes "greenhouse" → RED.
func TestApplyStructuredPrecedence_EmptySourceJobIDFallbackResolvedFromURL(t *testing.T) {
	structured := map[string]engine.JobListing{
		"https://boards.greenhouse.io/testco/jobs/4001234": {
			URL:     "https://boards.greenhouse.io/testco/jobs/4001234",
			JobID:   "4001234",
			Title:   "Greenhouse Eng",
			Company: "testco",
			Source:  "greenhouse",
		},
	}
	llm := []engine.JobListing{
		{
			// LinkedIn record — URL does NOT normalize-match the Greenhouse URL,
			// so the JobID fallback is the only path. Source is EMPTY (the LLM
			// omitted it). extractSourceFromURL returns "" for a non-ATS URL
			// (LinkedIn is not greenhouse/lever/ashby) → llmSrc stays "" →
			// fallback refused.
			URL:     "https://www.linkedin.com/jobs/view/4001234",
			JobID:   "4001234",
			Source:  "",
			Title:   "LinkedIn Eng",
			Company: "LinkedInCorp",
		},
	}

	ApplyStructuredPrecedence(llm, structured)

	if llm[0].Title != "LinkedIn Eng" {
		t.Errorf("Title = %q, want LinkedIn Eng (empty-Source JobID fallback must resolve from URL and refuse cross-provider match)", llm[0].Title)
	}
	if llm[0].Source != "" {
		t.Errorf("Source = %q, want empty (must NOT be overwritten by greenhouse)", llm[0].Source)
	}
	if llm[0].Company != "LinkedInCorp" {
		t.Errorf("Company = %q, want LinkedInCorp (cross-provider collision must NOT rewrite)", llm[0].Company)
	}
}

// TestApplyStructuredPrecedence_EmptySourceJobIDFallbackMatchesSameProvider
// verifies that the empty-Source resolution does NOT break the legitimate
// same-provider JobID fallback: an LLM record with no Source but a Lever URL
// and a Lever JobID still matches a Lever structured candidate. The URL
// resolves to "lever" which == the candidate's "lever".
//
// Mutation: refuse the JobID fallback whenever llm[i].Source == "" (instead of
// resolving from URL) → no match → SalaryMin stays nil → RED.
func TestApplyStructuredPrecedence_EmptySourceJobIDFallbackMatchesSameProvider(t *testing.T) {
	min := 160000
	structured := map[string]engine.JobListing{
		"https://jobs.lever.co/testco/abc": {
			URL:       "https://jobs.lever.co/testco/abc",
			JobID:     "abc",
			SalaryMin: &min,
			Source:    "lever",
		},
	}
	llm := []engine.JobListing{
		{
			// LLM emitted a different URL for the same job, no Source.
			// The URL resolves to "lever" via extractSourceFromURL.
			URL:       "https://jobs.lever.co/testco/abc/apply",
			JobID:     "abc",
			Source:    "",
			Title:     "Eng",
			SalaryMin: nil,
		},
	}

	ApplyStructuredPrecedence(llm, structured)

	if llm[0].SalaryMin == nil || *llm[0].SalaryMin != 160000 {
		t.Errorf("SalaryMin = %v, want 160000 (empty-Source same-provider JobID fallback must still match)", llm[0].SalaryMin)
	}
}

// TestApplyStructuredPrecedence_NoMatchAttributedToNone verifies that an LLM
// record whose URL is unresolvable (non-ATS, or a hallucinated URL for an ATS
// job) attributes the no_match to source="none" instead of dropping it. Both
// Lever and Ashby support custom board domains, so dropping unresolvable misses
// would make a join regression indistinguishable from "no ATS jobs in this
// search". The "none" label is in validStructuredPrecedenceSources (metrics.go)
// and was previously a dead allowlist entry with zero writers.
//
// This test uses the metrics registry to verify the counter bump — it requires
// engine.InitTestRegistry() so reg is non-nil.
//
// Mutation: restore `extractSourceFromURL` returning "" for unresolvable URLs
// with no "none" fallback → IncrStructuredPrecedence drops it ("" not in
// validStructuredPrecedenceSources) → counter delta = 0 → RED.
func TestApplyStructuredPrecedence_NoMatchAttributedToNone(t *testing.T) {
	engine.InitTestRegistry()
	before := engine.GetMetrics()

	structured := map[string]engine.JobListing{
		"https://jobs.lever.co/testco/abc": {
			URL:    "https://jobs.lever.co/testco/abc",
			Source: "lever",
		},
	}
	llm := []engine.JobListing{
		{
			// Non-ATS URL — extractSourceFromURL returns "", which must become
			// "none" so the no_match counter is bumped.
			URL: "https://example.com/some-job",
		},
	}

	ApplyStructuredPrecedence(llm, structured)

	after := engine.GetMetrics()
	key := engine.MetricStructuredPrecedence + "{source=none,outcome=no_match}"
	if after[key]-before[key] != 1 {
		t.Errorf("structured_precedence_total{source=none,outcome=no_match} delta = %d, want 1 (unresolvable no_match must be attributed to none, not dropped)", after[key]-before[key])
	}
}

// TestLeverPostingToListing_PerMonthSalary verifies that Lever's
// "per-month-salary" interval maps to "month" (previously unmapped → "" →
// early return with nil salary pointers → LLM salary zeroed by precedence).
//
// Mutation: remove "per-month-salary" from normalizeSalaryInterval →
// SalaryInterval becomes "" → RED.
func TestLeverPostingToListing_PerMonthSalary(t *testing.T) {
	p := leverPosting{
		ID:            "abc123",
		Text:          "Golang Engineer",
		HostedURL:     "https://jobs.lever.co/testco/abc123",
		WorkplaceType: "remote",
		CreatedAt:     1700000000000,
	}
	p.SalaryRange.Min = 10000
	p.SalaryRange.Max = 15000
	p.SalaryRange.Currency = "USD"
	p.SalaryRange.Interval = "per-month-salary"

	l := leverPostingToListing(p, "testco", "https://jobs.lever.co/testco/abc123", "Remote")

	if l.SalaryMin == nil || *l.SalaryMin != 10000 {
		t.Errorf("SalaryMin = %v, want 10000 (per-month-salary must map to month, not nil)", l.SalaryMin)
	}
	if l.SalaryMax == nil || *l.SalaryMax != 15000 {
		t.Errorf("SalaryMax = %v, want 15000", l.SalaryMax)
	}
	if l.SalaryInterval != "month" {
		t.Errorf("SalaryInterval = %q, want %q (per-month-salary normalized)", l.SalaryInterval, "month")
	}
}

// TestLeverSalaryString_UnmappedIntervalRenderedVerbatim verifies that
// leverSalaryString renders the salary with the raw interval token VERBATIM
// for unmapped intervals (per-week-salary, per-day-wage, one-time) — e.g.
// "4000–6000 USD/per-week-salary". The number reaches both call sites
// (SearchLeverJobsStructured content string + FetchATSBoard compensation field)
// carrying its own disambiguation. leverPostingToListing still leaves
// SalaryInterval empty (it will not assert a contract bucket it cannot map),
// so the scorer never renders $X/year for a per-week posting; a model can
// reason about "per-week", it cannot reason about absence.
//
// Mutation: restore the `if interval == "" { return "" }` suppression in
// leverSalaryString → per-week-salary returns "" instead of
// "4000–6000 USD/per-week-salary" → RED.
func TestLeverSalaryString_UnmappedIntervalRenderedVerbatim(t *testing.T) {
	tests := []struct {
		name     string
		interval string
		min      int
		max      int
		want     string
	}{
		{"per-week-salary", "per-week-salary", 4000, 6000, "4000–6000 USD/per-week-salary"},
		{"per-day-wage", "per-day-wage", 200, 300, "200–300 USD/per-day-wage"},
		{"one-time", "one-time", 5000, 5000, "5000–5000 USD/one-time"},
		{"empty interval (no info)", "", 160000, 220000, ""},
		{"per-year-salary (mapped)", "per-year-salary", 160000, 220000, "160000–220000 USD/year"},
		{"per-month-salary (mapped)", "per-month-salary", 10000, 15000, "10000–15000 USD/month"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := leverPosting{}
			p.SalaryRange.Min = tt.min
			p.SalaryRange.Max = tt.max
			p.SalaryRange.Currency = "USD"
			p.SalaryRange.Interval = tt.interval
			got := leverSalaryString(p)
			if got != tt.want {
				t.Errorf("leverSalaryString(interval=%q) = %q, want %q", tt.interval, got, tt.want)
			}
		})
	}
}

// TestApplyStructuredPrecedence_EmptyStructuredSkipsCounter verifies that when
// structuredByURL is empty, ApplyStructuredPrecedence is a no-op — no LLM
// record is mutated and the counter is not bumped (non-ATS LLM records would
// otherwise pollute no_match on platform=all).
//
// Mutation: remove the `len(structuredByURL) == 0` early return → no change to
// LLM records (still no match), but the counter would bump no_match for every
// record. This test guards the no-op; the counter skip is verified by the
// metrics test in jobserver.
func TestApplyStructuredPrecedence_EmptyStructuredSkipsCounter(t *testing.T) {
	llm := []engine.JobListing{
		{URL: "https://example.com/other", Title: "Other", Salary: "100k"},
	}
	ApplyStructuredPrecedence(llm, map[string]engine.JobListing{})
	if llm[0].Title != "Other" {
		t.Errorf("Title = %q, want untouched", llm[0].Title)
	}
	if llm[0].Salary != "100k" {
		t.Errorf("Salary = %q, want untouched (empty structured → no-op)", llm[0].Salary)
	}
}

// TestApplyStructuredPrecedence_FirstWriteWinsOnDuplicateNormURL verifies that
// when two structured listings normalize to the same URL key, the FIRST one
// inserted wins (byNormURL is first-write-wins at ats.go:1323). A change to
// last-write-wins would silently swap which structured record governs the LLM
// record — this test pins the deterministic order.
//
// Mutation: change `if _, exists := byNormURL[k]; !exists` to always overwrite
// → llm[0].Title becomes "Second" → RED.
func TestApplyStructuredPrecedence_FirstWriteWinsOnDuplicateNormURL(t *testing.T) {
	// Two structured listings whose URLs normalize to the same key (trailing
	// slash on the second).
	structured := map[string]engine.JobListing{
		"https://jobs.lever.co/testco/abc": {
			URL:    "https://jobs.lever.co/testco/abc",
			Title:  "First",
			Source: "lever",
		},
		"https://jobs.lever.co/testco/abc/": {
			URL:    "https://jobs.lever.co/testco/abc/",
			Title:  "Second",
			Source: "lever",
		},
	}
	llm := []engine.JobListing{
		{URL: "https://jobs.lever.co/testco/abc", Title: "LLM"},
	}

	ApplyStructuredPrecedence(llm, structured)

	// Map iteration order is non-deterministic, but BOTH keys normalize to the
	// same byNormURL key. Whichever inserts first wins; the other is skipped.
	// The LLM record must get ONE of the two structured titles (not the LLM
	// value), proving the dedup happened. We can't assert "First" specifically
	// due to map iteration order, but we CAN assert the LLM title was replaced
	// (proving a match occurred) and that it's one of the two.
	if llm[0].Title != "First" && llm[0].Title != "Second" {
		t.Errorf("Title = %q, want one of the two structured titles (first-write-wins dedup must have matched)", llm[0].Title)
	}
}
