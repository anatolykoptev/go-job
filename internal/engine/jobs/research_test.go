package jobs

import (
	"strings"
	"testing"
)

// --- isRussianLocation ---

func TestIsRussianLocation(t *testing.T) {
	tests := []struct {
		location string
		want     bool
	}{
		{"Москва", true},
		{"москва", true},
		{"Россия", true},
		{"россия", true},
		{"russia", true},
		{"moscow", true},
		{"saint-petersburg", true},
		{"спб", true},
		{"ru", true},
		{"San Francisco", false},
		{"Berlin", false},
		{"Remote", false},
		{"New York", false},
		{"", false},
		{"London", false},
		{"Paris", false},
	}
	for _, tt := range tests {
		t.Run(tt.location, func(t *testing.T) {
			got := isRussianLocation(tt.location)
			if got != tt.want {
				t.Errorf("isRussianLocation(%q) = %v, want %v", tt.location, got, tt.want)
			}
		})
	}
}

// --- buildSalaryQueries ---

func TestBuildSalaryQueries_International(t *testing.T) {
	queries := buildSalaryQueries("Senior Go Developer", "San Francisco", "senior")
	if len(queries) == 0 {
		t.Fatal("expected non-empty queries")
	}
	// Should contain levels.fyi or glassdoor for international
	combined := strings.Join(queries, " ")
	if !strings.Contains(combined, "levels.fyi") && !strings.Contains(combined, "glassdoor") {
		t.Errorf("international queries should reference levels.fyi or glassdoor, got: %v", queries)
	}
	// Should contain the role
	if !strings.Contains(combined, "Go Developer") {
		t.Errorf("queries should contain role, got: %v", queries)
	}
	// Should contain location
	if !strings.Contains(combined, "San Francisco") {
		t.Errorf("queries should contain location, got: %v", queries)
	}
}

func TestBuildSalaryQueries_Russian(t *testing.T) {
	queries := buildSalaryQueries("Backend Developer", "Москва", "mid")
	if len(queries) == 0 {
		t.Fatal("expected non-empty queries")
	}
	combined := strings.Join(queries, " ")
	// Should reference Russian job sites
	if !strings.Contains(combined, "hh.ru") && !strings.Contains(combined, "habr") {
		t.Errorf("Russian queries should reference hh.ru or habr, got: %v", queries)
	}
}

func TestBuildSalaryQueries_WithExperience(t *testing.T) {
	queries := buildSalaryQueries("Data Engineer", "Remote", "junior")
	combined := strings.Join(queries, " ")
	// Experience level should be included in queries
	if !strings.Contains(combined, "junior") {
		t.Errorf("queries should contain experience level, got: %v", queries)
	}
}

func TestBuildSalaryQueries_NoExperience(t *testing.T) {
	queries := buildSalaryQueries("Product Manager", "Berlin", "")
	if len(queries) == 0 {
		t.Fatal("expected non-empty queries even without experience")
	}
	combined := strings.Join(queries, " ")
	if !strings.Contains(combined, "Product Manager") {
		t.Errorf("queries should contain role, got: %v", queries)
	}
}

func TestBuildSalaryQueries_Count(t *testing.T) {
	// Should return exactly 3 queries for both international and Russian
	intlQueries := buildSalaryQueries("Go Developer", "New York", "senior")
	if len(intlQueries) != 3 {
		t.Errorf("expected 3 international queries, got %d", len(intlQueries))
	}

	ruQueries := buildSalaryQueries("Go Developer", "Москва", "senior")
	if len(ruQueries) != 3 {
		t.Errorf("expected 3 Russian queries, got %d", len(ruQueries))
	}
}

func TestBuildSalaryQueries_RussianLocation_Variants(t *testing.T) {
	ruLocations := []string{"Москва", "россия", "Russia", "Moscow", "saint-petersburg", "спб"}
	for _, loc := range ruLocations {
		queries := buildSalaryQueries("Developer", loc, "")
		combined := strings.Join(queries, " ")
		if !strings.Contains(combined, "hh.ru") && !strings.Contains(combined, "habr") {
			t.Errorf("location %q should produce RU queries with hh.ru/habr, got: %v", loc, queries)
		}
	}
}
