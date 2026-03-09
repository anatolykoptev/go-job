package jobs

import (
	"testing"
)

func TestExtractSkillsFromText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "golang and rust",
			text: "We need a Go developer with Rust experience to build a CLI tool",
			want: []string{"Go", "Rust", "CLI"},
		},
		{
			name: "web stack",
			text: "React frontend with TypeScript, Node.js backend, PostgreSQL database",
			want: []string{"React", "TypeScript", "Node.js", "PostgreSQL"},
		},
		{
			name: "scala zio",
			text: "Schema Migration System for ZIO Schema 2 using Scala macros",
			want: []string{"Scala", "ZIO"},
		},
		{
			name: "MCP and AI",
			text: "Incorporate MCP Server into the CLI for AI agent integration",
			want: []string{"MCP", "AI", "CLI"},
		},
		{
			name: "empty text",
			text: "",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractSkillsFromText(tt.text)
			for _, w := range tt.want {
				found := false
				for _, g := range got {
					if g == w {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("ExtractSkillsFromText() missing %q, got %v", w, got)
				}
			}
		})
	}
}

func TestMergeSkills(t *testing.T) {
	t.Parallel()

	got := MergeSkills(
		[]string{"Go", "Rust", "go"},
		[]string{"Python", "Go", "rust"},
	)
	if len(got) < 3 {
		t.Errorf("expected at least 3 unique skills, got %v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i] < got[i-1] {
			t.Errorf("not sorted: %v", got)
			break
		}
	}
}
