package engine

import "testing"

func TestExtractJSONAnswer(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "valid json",
			raw:  `{"answer": "hello world"}`,
			want: "hello world",
		},
		{
			name: "escaped quotes",
			raw:  `{"answer": "use \"fmt.Println\" for output"}`,
			want: `use "fmt.Println" for output`,
		},
		{
			name: "escaped newlines",
			raw:  `{"answer": "line1\nline2"}`,
			want: "line1\nline2",
		},
		{
			name: "no answer field",
			raw:  `{"result": "something"}`,
			want: "",
		},
		{
			name: "empty input",
			raw:  "",
			want: "",
		},
		{
			name: "malformed - no closing quote",
			raw:  `{"answer": "unclosed`,
			want: "unclosed",
		},
		{
			name: "extra whitespace",
			raw:  `{  "answer" :  "spaced out"  }`,
			want: "spaced out",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractJSONAnswer(tt.raw)
			if got != tt.want {
				t.Errorf("ExtractJSONAnswer() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildSourcesText(t *testing.T) {
	results := []SearxngResult{
		{Title: "Go Docs", URL: "https://go.dev/doc", Content: "Go is a language"},
		{Title: "Rust Docs", URL: "https://rust-lang.org", Content: "Rust is a language"},
	}
	contents := map[string]string{
		"https://go.dev/doc": "Full content of Go documentation page",
	}

	text := BuildSourcesText(results, contents, 1000)

	// Should include title and URL for both
	if !contains(text, "[1] Go Docs") {
		t.Error("missing first result title")
	}
	if !contains(text, "[2] Rust Docs") {
		t.Error("missing second result title")
	}

	// Should include fetched content for first result
	if !contains(text, "Content: Full content") {
		t.Error("missing fetched content")
	}

	// Should include snippet for second (no fetched content)
	if !contains(text, "Snippet: Rust is a language") {
		t.Error("missing snippet fallback")
	}

	// Should NOT include snippet when content exists
	if contains(text, "Snippet: Go is a language") {
		t.Error("snippet should not appear when content exists")
	}
}

func TestBuildSourcesTextTruncation(t *testing.T) {
	results := []SearxngResult{
		{Title: "Long", URL: "https://example.com", Content: "short"},
	}
	longContent := make([]byte, 500)
	for i := range longContent {
		longContent[i] = 'x'
	}
	contents := map[string]string{
		"https://example.com": string(longContent),
	}

	text := BuildSourcesText(results, contents, 100)
	// Content should be truncated with "..."
	if !contains(text, "...") {
		t.Error("expected truncation indicator")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsStr(s, substr)
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
