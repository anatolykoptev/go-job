package jobs

import (
	"github.com/anatolykoptev/go_job/internal/engine"
	"strings"
	"testing"
)

const sampleRemoteOKJSON = `[
	{"legal": "https://remoteok.com/legal"},
	{
		"slug": "remote-senior-go-developer-123",
		"id": "123",
		"position": "Senior Go Developer",
		"company": "Acme Corp",
		"tags": ["golang", "kubernetes", "docker"],
		"location": "Worldwide",
		"salary_min": 120000,
		"salary_max": 180000,
		"date": "2026-02-10T12:00:00+00:00",
		"url": "https://remoteok.com/remote-jobs/123"
	},
	{
		"slug": "remote-react-frontend-456",
		"id": "456",
		"position": "React Frontend Engineer",
		"company": "StartupXYZ",
		"tags": ["react", "typescript", "nextjs"],
		"location": "US Timezone",
		"salary_min": 0,
		"salary_max": 0,
		"date": "2026-02-08T10:00:00+00:00",
		"url": ""
	},
	{
		"slug": "",
		"id": "789",
		"position": "DevOps Engineer",
		"company": "CloudInc",
		"tags": ["aws", "terraform"],
		"location": "",
		"salary_min": 100000,
		"salary_max": 100000,
		"date": "",
		"url": ""
	}
]`

func TestParseRemoteOKResponse(t *testing.T) {
	jobs, err := parseRemoteOKResponse([]byte(sampleRemoteOKJSON))
	if err != nil {
		t.Fatalf("parseRemoteOKResponse error: %v", err)
	}

	if len(jobs) != 3 {
		t.Fatalf("expected 3 jobs, got %d", len(jobs))
	}

	// First job: full data
	j := jobs[0]
	if j.Title != "Senior Go Developer" {
		t.Errorf("title = %q, want %q", j.Title, "Senior Go Developer")
	}
	if j.Company != "Acme Corp" {
		t.Errorf("company = %q, want %q", j.Company, "Acme Corp")
	}
	if j.URL != "https://remoteok.com/remote-jobs/123" {
		t.Errorf("url = %q", j.URL)
	}
	if j.Source != "remoteok" {
		t.Errorf("source = %q, want remoteok", j.Source)
	}
	if j.Salary != "$120000 - $180000" {
		t.Errorf("salary = %q, want $120000 - $180000", j.Salary)
	}
	if j.Location != "Worldwide" {
		t.Errorf("location = %q, want Worldwide", j.Location)
	}
	if len(j.Tags) != 3 || j.Tags[0] != "golang" {
		t.Errorf("tags = %v, want [golang kubernetes docker]", j.Tags)
	}
	if j.Posted != "2026-02-10" {
		t.Errorf("posted = %q, want 2026-02-10", j.Posted)
	}

	// Second job: no salary, no URL (should use slug fallback)
	j2 := jobs[1]
	if j2.Salary != "not specified" {
		t.Errorf("salary = %q, want 'not specified'", j2.Salary)
	}
	if j2.URL != "https://remoteok.com/remote-jobs/remote-react-frontend-456" {
		t.Errorf("url = %q, want slug-based URL", j2.URL)
	}

	// Third job: same min/max salary
	j3 := jobs[2]
	if j3.Salary != "$100000" {
		t.Errorf("salary = %q, want $100000", j3.Salary)
	}
}

func TestParseRemoteOKResponseErrors(t *testing.T) {
	_, err := parseRemoteOKResponse([]byte(`invalid json`))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}

	// Only metadata element
	jobs, err := parseRemoteOKResponse([]byte(`[{"legal": "ok"}]`))
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(jobs) != 0 {
		t.Errorf("expected 0 jobs, got %d", len(jobs))
	}

	// Empty array
	jobs, err = parseRemoteOKResponse([]byte(`[]`))
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(jobs) != 0 {
		t.Errorf("expected 0 jobs, got %d", len(jobs))
	}
}

func TestFormatRemoteSalary(t *testing.T) {
	tests := []struct {
		name     string
		min, max int
		want     string
	}{
		{"range", 100000, 150000, "$100000 - $150000"},
		{"same", 80000, 80000, "$80000"},
		{"zero", 0, 0, "not specified"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatRemoteSalary(tt.min, tt.max)
			if got != tt.want {
				t.Errorf("formatRemoteSalary(%d, %d) = %q, want %q", tt.min, tt.max, got, tt.want)
			}
		})
	}
}

const sampleWWRRSS = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <item>
      <title>Acme Corp: Senior Backend Developer</title>
      <link>https://weworkremotely.com/remote-jobs/acme-corp-senior-backend</link>
      <pubDate>Mon, 10 Feb 2026 12:00:00 +0000</pubDate>
      <category>Programming</category>
      <type>Full-Time</type>
      <region>Anywhere in the World</region>
      <skills>Go, PostgreSQL, Docker</skills>
    </item>
    <item>
      <title>Plain Title Without Company</title>
      <link>https://weworkremotely.com/remote-jobs/plain-title</link>
      <pubDate>Sat, 08 Feb 2026 10:00:00 +0000</pubDate>
      <category>Design</category>
      <type></type>
      <region></region>
      <skills></skills>
    </item>
    <item>
      <title></title>
      <link>https://weworkremotely.com/empty</link>
    </item>
  </channel>
</rss>`

func TestParseWWRResponse(t *testing.T) {
	jobs, err := parseWWRResponse([]byte(sampleWWRRSS))
	if err != nil {
		t.Fatalf("parseWWRResponse error: %v", err)
	}

	// Empty title item should be skipped
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}

	// First job: "Company: Title" format
	j := jobs[0]
	if j.Title != "Senior Backend Developer" {
		t.Errorf("title = %q, want %q", j.Title, "Senior Backend Developer")
	}
	if j.Company != "Acme Corp" {
		t.Errorf("company = %q, want %q", j.Company, "Acme Corp")
	}
	if j.Source != "weworkremotely" {
		t.Errorf("source = %q, want weworkremotely", j.Source)
	}
	if j.Location != "Anywhere in the World" {
		t.Errorf("location = %q", j.Location)
	}
	if len(j.Tags) != 3 || j.Tags[0] != "Go" {
		t.Errorf("tags = %v, want [Go PostgreSQL Docker]", j.Tags)
	}
	if j.Posted != "2026-02-10" {
		t.Errorf("posted = %q, want 2026-02-10", j.Posted)
	}
	if j.JobType != "Full-Time" {
		t.Errorf("job_type = %q, want Full-Time", j.JobType)
	}
	if j.Salary != "not specified" {
		t.Errorf("salary = %q, want 'not specified'", j.Salary)
	}

	// Second job: no company prefix, defaults
	j2 := jobs[1]
	if j2.Title != "Plain Title Without Company" {
		t.Errorf("title = %q", j2.Title)
	}
	if j2.Company != "" {
		t.Errorf("company = %q, want empty", j2.Company)
	}
	if j2.Location != "Anywhere" {
		t.Errorf("location = %q, want Anywhere", j2.Location)
	}
	if j2.JobType != "remote" {
		t.Errorf("job_type = %q, want remote (default)", j2.JobType)
	}
}

func TestParseWWRResponseError(t *testing.T) {
	_, err := parseWWRResponse([]byte(`not xml`))
	if err == nil {
		t.Error("expected error for invalid XML")
	}
}

func TestParseWWRTitle(t *testing.T) {
	tests := []struct {
		raw     string
		title   string
		company string
	}{
		{"Acme Corp: Senior Developer", "Senior Developer", "Acme Corp"},
		{"Simple Title", "Simple Title", ""},
		{"A: B: C", "B: C", "A"},
	}
	for _, tt := range tests {
		title, company := parseWWRTitle(tt.raw)
		if title != tt.title || company != tt.company {
			t.Errorf("parseWWRTitle(%q) = (%q, %q), want (%q, %q)", tt.raw, title, company, tt.title, tt.company)
		}
	}
}

func TestFilterRemoteJobs(t *testing.T) {
	jobs := []engine.RemoteJobListing{
		{Title: "Senior Go Developer", Company: "Acme", Tags: []string{"golang", "kubernetes"}},
		{Title: "React Frontend Engineer", Company: "StartupXYZ", Tags: []string{"react", "typescript"}},
		{Title: "DevOps Engineer", Company: "CloudInc", Tags: []string{"aws", "terraform"}},
	}

	// Match by title
	filtered := filterRemoteJobs(jobs, "golang")
	if len(filtered) != 1 || filtered[0].Title != "Senior Go Developer" {
		t.Errorf("golang filter: got %d results", len(filtered))
	}

	// Match by tag
	filtered = filterRemoteJobs(jobs, "react")
	if len(filtered) != 1 || filtered[0].Title != "React Frontend Engineer" {
		t.Errorf("react filter: got %d results", len(filtered))
	}

	// Match by company
	filtered = filterRemoteJobs(jobs, "CloudInc")
	if len(filtered) != 1 || filtered[0].Title != "DevOps Engineer" {
		t.Errorf("CloudInc filter: got %d results", len(filtered))
	}

	// Multiple keywords: match any ("react" matches ReactFE, "developer" matches Go)
	filtered = filterRemoteJobs(jobs, "react developer")
	if len(filtered) != 2 {
		t.Errorf("multi keyword filter: got %d results, want 2", len(filtered))
	}

	// Empty query: return all
	filtered = filterRemoteJobs(jobs, "")
	if len(filtered) != 3 {
		t.Errorf("empty query: got %d results, want 3", len(filtered))
	}

	// No match
	filtered = filterRemoteJobs(jobs, "python django")
	if len(filtered) != 0 {
		t.Errorf("no match: got %d results, want 0", len(filtered))
	}
}

func TestRemoteJobsToSearxngResults(t *testing.T) {
	jobs := []engine.RemoteJobListing{
		{
			Title:    "Go Developer",
			Company:  "Acme",
			URL:      "https://example.com/job/1",
			Source:   "remoteok",
			Salary:   "$120000 - $180000",
			Location: "Worldwide",
			Tags:     []string{"golang", "docker"},
			Posted:   "2026-02-10",
			JobType:  "remote",
		},
	}

	results := RemoteJobsToSearxngResults(jobs)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	r := results[0]
	if r.Score != 1.0 {
		t.Errorf("score = %f, want 1.0", r.Score)
	}
	if r.URL != "https://example.com/job/1" {
		t.Errorf("url = %q", r.URL)
	}
	if !strings.Contains(r.Title, "Acme") {
		t.Errorf("title should contain company, got: %s", r.Title)
	}
	if !strings.Contains(r.Content, "$120000 - $180000") {
		t.Errorf("content should contain salary, got: %s", r.Content)
	}
	if !strings.Contains(r.Content, "golang, docker") {
		t.Errorf("content should contain tags, got: %s", r.Content)
	}
	if !strings.Contains(r.Content, "remoteok") {
		t.Errorf("content should contain source, got: %s", r.Content)
	}
}
