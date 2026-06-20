package jobs

import (
	"os"
	"strings"
	"testing"
)

func TestParseAlgoraJobURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantOrg string
		wantID  string
		wantOK  bool
	}{
		{"valid job", "https://algora.io/comfy/job/cz9bpQrBC38UDigM", "comfy", "cz9bpQrBC38UDigM", true},
		{"valid job with trailing slash", "https://algora.io/myorg/job/abc123/", "myorg", "abc123", true},
		{"bounty url", "https://algora.io/bounties", "", "", false},
		{"board url", "https://algora.io/comfy/jobs", "", "", false},
		{"garbage", "not a url", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			org, id, ok := parseAlgoraJobURL(tc.url)
			if ok != tc.wantOK || org != tc.wantOrg || id != tc.wantID {
				t.Errorf("parseAlgoraJobURL(%q) = (%q,%q,%v) want (%q,%q,%v)",
					tc.url, org, id, ok, tc.wantOrg, tc.wantID, tc.wantOK)
			}
		})
	}
}

func TestParseAlgoraJob_Tier1(t *testing.T) {
	htmlBody := `<html><head>
        <meta property="og:title" content="ComfyUI Senior Software Engineer (Backend)" />
        <meta property="og:url" content="https://algora.io/comfy/job/cz9bpQrBC38UDigM" />
    </head><body></body></html>`
	j, err := parseAlgoraJob(htmlBody, "https://algora.io/comfy/job/cz9bpQrBC38UDigM")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if j.Source != "algora-jobs" {
		t.Errorf("Source=%q want algora-jobs", j.Source)
	}
	if j.JobType != "job" {
		t.Errorf("JobType=%q want job", j.JobType)
	}
	if j.JobID != "cz9bpQrBC38UDigM" {
		t.Errorf("JobID=%q want cz9bpQrBC38UDigM", j.JobID)
	}
	if j.Title != "ComfyUI Senior Software Engineer (Backend)" {
		t.Errorf("Title=%q unexpected", j.Title)
	}
}

func TestParseAlgoraJobRows_SplitSalary(t *testing.T) {
	htmlBody := `<html><body>
    <div class="flex justify-between border-b">
      <span class="text-muted-foreground">Base Salary</span>
      <span><span>$150k - </span><span>$300k</span></span>
    </div>
    <div class="flex justify-between border-b">
      <span class="text-muted-foreground">Location</span>
      <span>San Francisco</span>
    </div>
    <div class="flex justify-between border-b">
      <span class="text-muted-foreground">Equity</span>
      <span>Competitive</span>
    </div>
    </body></html>`
	j, err := parseAlgoraJob(htmlBody, "https://algora.io/comfy/job/abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if j.SalaryMin == nil || *j.SalaryMin != 150000 {
		t.Errorf("SalaryMin=%v want 150000", j.SalaryMin)
	}
	if j.SalaryMax == nil || *j.SalaryMax != 300000 {
		t.Errorf("SalaryMax=%v want 300000", j.SalaryMax)
	}
	if j.SalaryCurrency != "USD" {
		t.Errorf("SalaryCurrency=%q want USD", j.SalaryCurrency)
	}
	if j.SalaryInterval != "year" {
		t.Errorf("SalaryInterval=%q want year", j.SalaryInterval)
	}
	if j.Location != "San Francisco" {
		t.Errorf("Location=%q want San Francisco", j.Location)
	}
	hasEquityTag := false
	for _, tag := range j.Skills {
		if strings.Contains(strings.ToLower(tag), "equity") {
			hasEquityTag = true
		}
	}
	if !hasEquityTag {
		t.Errorf("equity tag missing from Skills/tags; got %v", j.Skills)
	}
}

func TestParseAlgoraJobRows_FreeTextEquity(t *testing.T) {
	htmlBody := `<html><body>
    <div class="flex justify-between border-b">
      <span class="text-muted-foreground">Equity</span>
      <span>Competitive</span>
    </div>
    </body></html>`
	j, _ := parseAlgoraJob(htmlBody, "https://algora.io/comfy/job/abc123")
	if j.SalaryMin != nil {
		t.Errorf("SalaryMin should be nil for equity-only row, got %d", *j.SalaryMin)
	}
}

func TestParseAlgoraJob_MissingSalary(t *testing.T) {
	htmlBody := `<html><head>
        <meta property="og:title" content="Some Job" />
        <meta property="og:url" content="https://algora.io/org/job/id1" />
    </head><body></body></html>`
	j, err := parseAlgoraJob(htmlBody, "https://algora.io/org/job/id1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if j.SalaryMin != nil || j.SalaryMax != nil {
		t.Errorf("SalaryMin/Max should be nil when salary row absent")
	}
}

func TestParseAlgoraJob_DescriptionIsProseNotTagline(t *testing.T) {
	const tagline = "The most powerful open source node-based application for generative AI"
	const jobDesc = "We are looking for a senior backend engineer to work on our inference pipeline."
	htmlBody := `<html><head>
        <meta property="og:title" content="Senior Engineer" />
        <meta property="og:url" content="https://algora.io/comfy/job/abc123" />
        <meta property="og:description" content="` + tagline + `" />
    </head><body>
    <div class="prose prose-invert">
      <p>` + jobDesc + `</p>
    </div>
    </body></html>`
	j, err := parseAlgoraJob(htmlBody, "https://algora.io/comfy/job/abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(j.Description, tagline) {
		t.Errorf("Description contains company tagline (og:description leak): %q", j.Description)
	}
	if !strings.Contains(j.Description, "inference pipeline") {
		t.Errorf("Description should contain prose body text, got: %q", j.Description)
	}
	if j.Description == "" {
		t.Errorf("Description is empty — should come from div.prose.prose-invert")
	}
}

func TestParseAlgoraJob_Malformed(t *testing.T) {
	tests := []struct {
		name string
		html string
	}{
		{"empty", ""},
		{"garbage", "not html at all <><>"},
		{"no og tags", "<html><body><p>nothing</p></body></html>"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			j, err := parseAlgoraJob(tc.html, "https://algora.io/comfy/job/abc")
			_ = j
			_ = err
		})
	}
}

func TestAlgoraJobIngestInput_Validation(t *testing.T) {
	validURL := "https://algora.io/comfy/job/cz9bpQrBC38UDigM"
	_, _, ok := parseAlgoraJobURL(validURL)
	if !ok {
		t.Error("valid algora job URL rejected")
	}
	invalidURL := "https://algora.io/comfy/jobs"
	_, _, ok = parseAlgoraJobURL(invalidURL)
	if ok {
		t.Error("board URL should be rejected")
	}
}

func TestParseAlgoraJob_ComfyFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/algora_job_comfy.html")
	if err != nil {
		t.Fatalf("fixture missing — regenerate with: curl -sL -A chrome https://algora.io/comfy/job/cz9bpQrBC38UDigM > testdata/algora_job_comfy.html: %v", err)
	}
	const jobURL = "https://algora.io/comfy/job/cz9bpQrBC38UDigM"
	j, err := parseAlgoraJob(string(data), jobURL)
	if err != nil {
		t.Fatalf("parseAlgoraJob error: %v", err)
	}

	// Source + type invariants.
	if j.Source != "algora-jobs" {
		t.Errorf("Source=%q want algora-jobs", j.Source)
	}
	if j.JobType != "job" {
		t.Errorf("JobType=%q want job", j.JobType)
	}
	if j.JobID != "cz9bpQrBC38UDigM" {
		t.Errorf("JobID=%q want cz9bpQrBC38UDigM", j.JobID)
	}

	// Company: the live Algora page renders "ComfyUI" in the org header span
	// (class "font-semibold text-foreground"). The spec Open Decision 1 assumed
	// "Comfy Org" but the real markup has "ComfyUI". Asserted against live fixture.
	if j.Company != "ComfyUI" {
		t.Errorf("Company=%q want ComfyUI (as rendered in org header span)", j.Company)
	}

	// Salary (Tier-2 row-walk): $150k - $300k -> 150000 / 300000 USD/year.
	if j.SalaryMin == nil || *j.SalaryMin != 150000 {
		t.Errorf("SalaryMin=%v want 150000", j.SalaryMin)
	}
	if j.SalaryMax == nil || *j.SalaryMax != 300000 {
		t.Errorf("SalaryMax=%v want 300000", j.SalaryMax)
	}
	if j.SalaryCurrency != "USD" {
		t.Errorf("SalaryCurrency=%q want USD", j.SalaryCurrency)
	}

	// Location (Tier-2).
	if j.Location != "San Francisco" {
		t.Errorf("Location=%q want San Francisco", j.Location)
	}

	// Description must come from the LONGEST prose-invert block (the job description),
	// NOT og:description (company tagline) and NOT the short org pitch block.
	const ogTagline = "node-based application for generative AI"
	if strings.Contains(j.Description, ogTagline) {
		t.Errorf("Description contains og:description tagline (regression): %q", j.Description)
	}
	if !strings.Contains(j.Description, "The Role") && !strings.Contains(j.Description, "backend") {
		t.Errorf("Description does not come from job requirements block; got: %q",
			j.Description[:minLen(200, len(j.Description))])
	}
	if j.Description == "" {
		t.Errorf("Description is empty")
	}
}

func minLen(a, b int) int {
	if a < b {
		return a
	}
	return b
}
