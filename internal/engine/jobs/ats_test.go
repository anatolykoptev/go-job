package jobs

import (
	"github.com/anatolykoptev/go_job/internal/engine"
	"strings"
	"testing"
)

// --- extractGreenhouseSlugs ---

func TestExtractGreenhouseSlugs(t *testing.T) {
	tests := []struct {
		name    string
		results []engine.SearxngResult
		want    []string
	}{
		{
			name: "standard job board URL",
			results: []engine.SearxngResult{
				{URL: "https://boards.greenhouse.io/stripe/jobs/123456"},
				{URL: "https://boards.greenhouse.io/anthropic/jobs/789"},
			},
			want: []string{"stripe", "anthropic"},
		},
		{
			name: "dedup same slug",
			results: []engine.SearxngResult{
				{URL: "https://boards.greenhouse.io/stripe/jobs/111"},
				{URL: "https://boards.greenhouse.io/stripe/jobs/222"},
				{URL: "https://boards.greenhouse.io/airbnb/jobs/333"},
			},
			want: []string{"stripe", "airbnb"},
		},
		{
			name: "URL with query params",
			results: []engine.SearxngResult{
				{URL: "https://boards.greenhouse.io/openai/jobs/5678?gh_src=abc"},
			},
			want: []string{"openai"},
		},
		{
			name: "non-greenhouse URLs ignored",
			results: []engine.SearxngResult{
				{URL: "https://jobs.lever.co/stripe/abc"},
				{URL: "https://www.linkedin.com/jobs/view/123"},
				{URL: "https://boards.greenhouse.io/figma/jobs/999"},
			},
			want: []string{"figma"},
		},
		{
			name:    "empty input",
			results: []engine.SearxngResult{},
			want:    nil,
		},
		{
			name: "slug normalized to lowercase",
			results: []engine.SearxngResult{
				{URL: "https://boards.greenhouse.io/AcmeCorp/jobs/1"},
			},
			want: []string{"acmecorp"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractGreenhouseSlugs(tt.results)
			if len(got) != len(tt.want) {
				t.Fatalf("extractGreenhouseSlugs() = %v, want %v", got, tt.want)
			}
			for i, s := range got {
				if s != tt.want[i] {
					t.Errorf("[%d] = %q, want %q", i, s, tt.want[i])
				}
			}
		})
	}
}

// --- extractLeverSlugs ---

func TestExtractLeverSlugs(t *testing.T) {
	tests := []struct {
		name    string
		results []engine.SearxngResult
		want    []string
	}{
		{
			name: "standard lever URL",
			results: []engine.SearxngResult{
				{URL: "https://jobs.lever.co/stripe/abc-def-123"},
				{URL: "https://jobs.lever.co/anthropic/xyz-789"},
			},
			want: []string{"stripe", "anthropic"},
		},
		{
			name: "dedup",
			results: []engine.SearxngResult{
				{URL: "https://jobs.lever.co/openai/job1"},
				{URL: "https://jobs.lever.co/openai/job2"},
			},
			want: []string{"openai"},
		},
		{
			name: "non-lever URLs ignored",
			results: []engine.SearxngResult{
				{URL: "https://boards.greenhouse.io/stripe"},
				{URL: "https://jobs.lever.co/figma/abc"},
			},
			want: []string{"figma"},
		},
		{
			name:    "empty",
			results: []engine.SearxngResult{},
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractLeverSlugs(tt.results)
			if len(got) != len(tt.want) {
				t.Fatalf("extractLeverSlugs() = %v, want %v", got, tt.want)
			}
			for i, s := range got {
				if s != tt.want[i] {
					t.Errorf("[%d] = %q, want %q", i, s, tt.want[i])
				}
			}
		})
	}
}

// --- matchesKeywords ---

func TestMatchesKeywords(t *testing.T) {
	tests := []struct {
		name     string
		haystack string
		keywords []string
		want     bool
	}{
		{
			name:     "single keyword match",
			haystack: "Senior Go Developer at Stripe",
			keywords: []string{"golang"},
			want:     false,
		},
		{
			name:     "exact match",
			haystack: "Senior Go Developer at Stripe",
			keywords: []string{"go"},
			want:     true,
		},
		{
			name:     "case insensitive",
			haystack: "Senior Go Developer at Stripe",
			keywords: []string{"STRIPE"},
			want:     true,
		},
		{
			name:     "any keyword matches",
			haystack: "Python Engineer at Meta",
			keywords: []string{"golang", "python", "rust"},
			want:     true,
		},
		{
			name:     "no match",
			haystack: "Python Engineer at Meta",
			keywords: []string{"golang", "rust", "java"},
			want:     false,
		},
		{
			name:     "empty keywords returns true",
			haystack: "anything",
			keywords: []string{},
			want:     true,
		},
		{
			name:     "nil keywords returns true",
			haystack: "anything",
			keywords: nil,
			want:     true,
		},
		{
			name:     "empty haystack",
			haystack: "",
			keywords: []string{"go"},
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesKeywords(tt.haystack, tt.keywords)
			if got != tt.want {
				t.Errorf("matchesKeywords(%q, %v) = %v, want %v", tt.haystack, tt.keywords, got, tt.want)
			}
		})
	}
}

// --- extractATSCompanyName ---

func TestExtractATSCompanyName(t *testing.T) {
	tests := []struct {
		name   string
		rawURL string
		want   string
	}{
		{
			name:   "greenhouse URL",
			rawURL: "https://boards.greenhouse.io/stripe/jobs/12345",
			want:   "stripe",
		},
		{
			name:   "lever URL",
			rawURL: "https://jobs.lever.co/anthropic/abc-123",
			want:   "anthropic",
		},
		{
			name:   "generic URL with path",
			rawURL: "https://careers.example.com/openai/jobs",
			want:   "openai",
		},
		{
			name:   "empty URL",
			rawURL: "",
			want:   "",
		},
		{
			name:   "root path only",
			rawURL: "https://example.com/",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractATSCompanyName(tt.rawURL)
			if got != tt.want {
				t.Errorf("extractATSCompanyName(%q) = %q, want %q", tt.rawURL, got, tt.want)
			}
		})
	}
}

// --- Greenhouse result content format ---

func TestGreenhouseResultContent(t *testing.T) {
	// Test that a Greenhouse result has expected content structure
	// (simulates what SearchGreenhouseJobs builds)
	slug := "stripe"
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

	jobURL := job.AbsoluteURL
	content := "**Source:** Greenhouse | **Company:** " + slug + " | **Location:** " + job.Location.Name
	if len(job.Departments) > 0 {
		content += " | **Dept:** " + job.Departments[0].Name
	}
	if job.UpdatedAt != "" && len(job.UpdatedAt) >= 10 {
		content += " | **Updated:** " + job.UpdatedAt[:10]
	}

	result := engine.SearxngResult{
		Title:   job.Title,
		Content: content,
		URL:     jobURL,
		Score:   0.9,
	}

	if result.Title != "Senior Go Engineer" {
		t.Errorf("title = %q", result.Title)
	}
	if !strings.Contains(result.Content, "**Source:** Greenhouse") {
		t.Errorf("missing source tag: %s", result.Content)
	}
	if !strings.Contains(result.Content, "**Company:** stripe") {
		t.Errorf("missing company: %s", result.Content)
	}
	if !strings.Contains(result.Content, "**Location:** Remote") {
		t.Errorf("missing location: %s", result.Content)
	}
	if !strings.Contains(result.Content, "**Dept:** Engineering") {
		t.Errorf("missing dept: %s", result.Content)
	}
	if !strings.Contains(result.Content, "**Updated:** 2026-02-10") {
		t.Errorf("missing updated date: %s", result.Content)
	}
	if result.Score != 0.9 {
		t.Errorf("score = %f, want 0.9", result.Score)
	}
}

// --- Lever result content format ---

func TestLeverResultContent(t *testing.T) {
	slug := "anthropic"
	p := leverPosting{
		ID:               "abc-def-123",
		Text:             "AI Safety Researcher",
		HostedURL:        "https://jobs.lever.co/anthropic/abc-def-123",
		DescriptionPlain: "Work on AI safety research at Anthropic.",
	}
	p.Categories.Location = "San Francisco, CA"
	p.Categories.Team = "Research"
	p.Categories.Commitment = "Full-time"

	content := "**Source:** Lever | **Company:** " + slug + " | **Location:** " + p.Categories.Location
	content += " | **Team:** " + p.Categories.Team
	content += " | **Type:** " + p.Categories.Commitment
	content += "\n\n" + p.DescriptionPlain

	result := engine.SearxngResult{
		Title:   p.Text,
		Content: content,
		URL:     p.HostedURL,
		Score:   0.9,
	}

	if result.Title != "AI Safety Researcher" {
		t.Errorf("title = %q", result.Title)
	}
	if !strings.Contains(result.Content, "**Source:** Lever") {
		t.Errorf("missing source: %s", result.Content)
	}
	if !strings.Contains(result.Content, "Research") {
		t.Errorf("missing team: %s", result.Content)
	}
	if !strings.Contains(result.Content, "Full-time") {
		t.Errorf("missing commitment: %s", result.Content)
	}
	if !strings.Contains(result.Content, "AI safety research") {
		t.Errorf("missing description: %s", result.Content)
	}
}
