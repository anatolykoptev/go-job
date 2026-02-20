package engine

import "testing"

func TestDetectQueryType(t *testing.T) {
	tests := []struct {
		query string
		want  QueryType
	}{
		{"сколько стоит биткоин", QtFact},
		{"what is the population of France", QtFact},
		{"price of gold", QtFact},
		{"React vs Vue", QtComparison},
		{"сравни Go и Rust", QtComparison},
		{"what is the difference between TCP and UDP", QtFact}, // "what is the" matches fact before "difference" matches comparison
		{"top 10 go frameworks", QtList},
		{"лучшие инструменты для CI/CD", QtList},
		{"best alternatives to Webpack", QtList},
		{"how to setup nginx", QtHowTo},
		{"как настроить docker compose", QtHowTo},
		{"step by step guide to deploy", QtHowTo},
		{"golang context package", QtGeneral},
		{"MCP protocol overview", QtGeneral},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			got := DetectQueryType(tt.query)
			if got != tt.want {
				t.Errorf("DetectQueryType(%q) = %d, want %d", tt.query, got, tt.want)
			}
		})
	}
}

func TestDetectQueryDomain(t *testing.T) {
	// Init with minimal config so ExtractLibraryName doesn't panic
	Init(Config{})

	tests := []struct {
		query string
		want  QueryDomain
	}{
		{"wordpress add_action hook", QdWordPress},
		{"wp_enqueue_script usage", QdWordPress},
		{"gutenberg block development", QdWordPress},
		{"claude code plugin hooks", QdClaudeCode},
		{"pretooluse event handler", QdClaudeCode},
		{"library for parsing JSON in Go", QdGitHubRepo},
		{"best library for HTTP in Python", QdGitHubRepo},
		{"alternatives to lodash", QdGitHubRepo},
		{"react useEffect cleanup", QdLibDocs},
		{"fastapi dependency injection", QdLibDocs},
		{"random unrelated query about cats", QdGeneral},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			got := DetectQueryDomain(tt.query)
			if got != tt.want {
				t.Errorf("DetectQueryDomain(%q) = %d, want %d", tt.query, got, tt.want)
			}
		})
	}
}
