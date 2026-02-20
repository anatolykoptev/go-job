package sources

import (
	"github.com/anatolykoptev/go_job/internal/engine"
	"strings"
	"testing"
)

const sampleFreelancerJSON = `{
	"status": "success",
	"result": {
		"projects": [
			{
				"id": 12345,
				"title": "Build REST API in Go",
				"seo_url": "build-rest-api-in-go",
				"preview_description": "Need a Go developer to build a REST API with authentication and database integration.",
				"budget": {"minimum": 500, "maximum": 1500},
				"currency": {"code": "USD", "sign": "$"},
				"bid_stats": {"bid_count": 12, "bid_avg": 850.5},
				"jobs": [
					{"id": 1, "name": "Go"},
					{"id": 2, "name": "REST API"},
					{"id": 3, "name": "PostgreSQL"}
				],
				"type": "fixed",
				"time_submitted": 1739500000
			},
			{
				"id": 67890,
				"title": "Hourly React Developer",
				"seo_url": "hourly-react-developer",
				"preview_description": "Looking for a React developer for ongoing frontend work.",
				"budget": {"minimum": 30, "maximum": 60},
				"currency": {"code": "USD", "sign": "$"},
				"bid_stats": {"bid_count": 5, "bid_avg": 45.0},
				"jobs": [
					{"id": 4, "name": "React"},
					{"id": 5, "name": "TypeScript"}
				],
				"type": "hourly",
				"time_submitted": 1739400000
			},
			{
				"id": 11111,
				"title": "No Budget Project",
				"seo_url": "no-budget-project",
				"preview_description": "Small task.",
				"budget": {"minimum": 0, "maximum": 0},
				"currency": {"code": "EUR", "sign": ""},
				"bid_stats": {"bid_count": 0, "bid_avg": 0},
				"jobs": [],
				"type": "fixed",
				"time_submitted": 0
			}
		]
	}
}`

func TestParseFreelancerResponse(t *testing.T) {
	projects, err := parseFreelancerResponse([]byte(sampleFreelancerJSON))
	if err != nil {
		t.Fatalf("parseFreelancerResponse error: %v", err)
	}

	if len(projects) != 3 {
		t.Fatalf("expected 3 projects, got %d", len(projects))
	}

	// First project: fixed budget
	p := projects[0]
	if p.Title != "Build REST API in Go" {
		t.Errorf("title = %q, want %q", p.Title, "Build REST API in Go")
	}
	if p.URL != "https://www.freelancer.com/projects/build-rest-api-in-go" {
		t.Errorf("url = %q", p.URL)
	}
	if p.Platform != "freelancer" {
		t.Errorf("platform = %q, want freelancer", p.Platform)
	}
	if p.Budget != "$500-$1500 USD" {
		t.Errorf("budget = %q, want $500-$1500 USD", p.Budget)
	}
	if len(p.Skills) != 3 {
		t.Errorf("skills count = %d, want 3", len(p.Skills))
	}
	if p.Skills[0] != "Go" {
		t.Errorf("skills[0] = %q, want Go", p.Skills[0])
	}
	if p.Posted == "" {
		t.Error("posted should not be empty")
	}

	// Second project: hourly
	p2 := projects[1]
	if p2.Budget != "$30-$60 USD/hr" {
		t.Errorf("hourly budget = %q, want $30-$60 USD/hr", p2.Budget)
	}

	// Third project: no budget
	p3 := projects[2]
	if p3.Budget != "not specified" {
		t.Errorf("no budget = %q, want 'not specified'", p3.Budget)
	}
	if p3.Posted != "" {
		t.Errorf("posted for zero timestamp should be empty, got %q", p3.Posted)
	}
}

func TestParseFreelancerResponseError(t *testing.T) {
	_, err := parseFreelancerResponse([]byte(`{"status": "error"}`))
	if err == nil {
		t.Error("expected error for non-success status")
	}

	_, err = parseFreelancerResponse([]byte(`invalid json`))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestFreelancerProjectsToSearxngResults(t *testing.T) {
	projects := []engine.FreelanceProject{
		{
			Title:       "Test Project",
			URL:         "https://www.freelancer.com/projects/test",
			Platform:    "freelancer",
			Budget:      "$100-$500 USD",
			Skills:      []string{"Go", "Docker"},
			Description: "Build a microservice",
			Posted:      "2025-02-14",
		},
	}

	results := FreelancerProjectsToSearxngResults(projects)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	r := results[0]
	if r.Score != 1.0 {
		t.Errorf("score = %f, want 1.0", r.Score)
	}
	if r.URL != "https://www.freelancer.com/projects/test" {
		t.Errorf("url = %q", r.URL)
	}
	if !strings.Contains(r.Content, "$100-$500 USD") {
		t.Errorf("content should contain budget, got: %s", r.Content)
	}
	if !strings.Contains(r.Content, "Go, Docker") {
		t.Errorf("content should contain skills, got: %s", r.Content)
	}
	if !strings.Contains(r.Content, "Build a microservice") {
		t.Errorf("content should contain description, got: %s", r.Content)
	}
}

func TestFormatBudget(t *testing.T) {
	tests := []struct {
		name     string
		budget   freelancerBudget
		currency freelancerCurrency
		pType    string
		want     string
	}{
		{
			name:     "fixed range",
			budget:   freelancerBudget{Minimum: 100, Maximum: 500},
			currency: freelancerCurrency{Code: "USD", Sign: "$"},
			pType:    "fixed",
			want:     "$100-$500 USD",
		},
		{
			name:     "hourly range",
			budget:   freelancerBudget{Minimum: 25, Maximum: 50},
			currency: freelancerCurrency{Code: "USD", Sign: "$"},
			pType:    "hourly",
			want:     "$25-$50 USD/hr",
		},
		{
			name:     "same min max",
			budget:   freelancerBudget{Minimum: 300, Maximum: 300},
			currency: freelancerCurrency{Code: "EUR", Sign: ""},
			pType:    "fixed",
			want:     "300 EUR",
		},
		{
			name:     "zero budget",
			budget:   freelancerBudget{Minimum: 0, Maximum: 0},
			currency: freelancerCurrency{Code: "USD", Sign: "$"},
			pType:    "fixed",
			want:     "not specified",
		},
		{
			name:     "empty currency code",
			budget:   freelancerBudget{Minimum: 100, Maximum: 200},
			currency: freelancerCurrency{Code: "", Sign: ""},
			pType:    "fixed",
			want:     "100-200 USD",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatBudget(tt.budget, tt.currency, tt.pType)
			if got != tt.want {
				t.Errorf("formatBudget() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTruncateDescription(t *testing.T) {
	short := "Hello world"
	if got := engine.TruncateAtWord(short, 300); got != short {
		t.Errorf("short string should not be truncated, got %q", got)
	}

	long := "This is a very long description that needs to be truncated at a word boundary for readability"
	result := engine.TruncateAtWord(long, 50)
	if !strings.HasSuffix(result, "...") {
		t.Errorf("truncated string should end with '...', got %q", result)
	}
	// Should truncate at word boundary before 50 runes
	withoutEllipsis := strings.TrimSuffix(result, "...")
	if len([]rune(withoutEllipsis)) > 50 {
		t.Errorf("truncated rune count = %d, should be <= 50", len([]rune(withoutEllipsis)))
	}

	// Test with multi-byte characters (Cyrillic)
	cyrillic := "Разработка REST API для мобильного приложения с интеграцией базы данных и аутентификацией"
	cyrResult := engine.TruncateAtWord(cyrillic, 30)
	if !strings.HasSuffix(cyrResult, "...") {
		t.Errorf("cyrillic truncation should end with '...', got %q", cyrResult)
	}
	// Verify no corrupted UTF-8
	for _, r := range cyrResult {
		if r == '\uFFFD' {
			t.Error("truncated cyrillic contains replacement character (corrupted UTF-8)")
		}
	}
}
