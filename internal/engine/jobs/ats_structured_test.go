package jobs

// ats_structured_test.go guards the structured JobListing mappers for the three
// ATS adapters (greenhouse, lever, ashby) and the URL normalization helper
// (NormalizeURL) shared by the live join path.
//
// The join-precedence invariants that ApplyStructuredPrecedence encoded are
// now asserted against the live path (buildHealthySelection) in
// tool_job_search_spine_test.go (P1–P5). This file keeps the mapper tests and
// NormalizeURL, which test live functions directly.
//
// Every assertion is an EXACT value (anti-vacuous): each test MUST go RED when
// the production mapping it guards is reverted. The mutation probe per test is
// documented in its comment.

import (
	"testing"
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

// TestNormalizeURL verifies the URL canonicalization that makes the
// structuredByURL join key match across producer and lookup sides.
//
// Mutation: stop stripping trailing slash → "https://x/jobs/1/" and
// "https://x/jobs/1" no longer match → spine P1 (URLNormalizationMatch) RED.
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

// TestLeverPostingToListing_PerMonthSalary verifies that Lever's
// "per-month-salary" interval maps to "month" (previously unmapped → "" →
// early return with nil salary pointers, so the structured listing carried no
// numeric comp).
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
