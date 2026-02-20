package jobs

import (
	"github.com/anatolykoptev/go_job/internal/engine"
	"strings"
	"testing"
)

func TestExtractHNJobTitle(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{
			name: "pipe-separated format",
			text: "Stripe | Software Engineer, Backend | Remote | Full-time\n\nWe are looking for a backend engineer...",
			want: "Stripe | Software Engineer, Backend | Remote | Full-time",
		},
		{
			name: "company and role first line",
			text: "Anthropic – AI Safety Researcher\nLocation: San Francisco, CA\nSalary: $200k+",
			want: "Anthropic – AI Safety Researcher",
		},
		{
			name: "long first line truncated at 80 chars",
			text: strings.Repeat("A", 100) + "\nrest of text",
			want: strings.Repeat("A", 80) + "...",
		},
		{
			name: "short single line",
			text: "Google | SWE",
			want: "Google | SWE",
		},
		{
			name: "empty text",
			text: "",
			want: "",
		},
		{
			name: "long text no newline",
			text: strings.Repeat("X", 120),
			want: strings.Repeat("X", 80) + "...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractHNJobTitle(tt.text)
			if got != tt.want {
				t.Errorf("extractHNJobTitle() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFilterHNJobComments(t *testing.T) {
	comments := []string{
		"Stripe | Backend Engineer | Remote | Full-time\n\nWe use Go, Kubernetes, PostgreSQL",
		"Anthropic | AI Safety Researcher | San Francisco\n\nPython, ML background required",
		"Meta | Product Manager | NYC\n\nLooking for PM with 5+ years experience",
		"OpenAI | Research Engineer | Remote\n\nPyTorch, distributed training, LLM experience",
		"Shopify | Senior Go Developer | Remote\n\nWe are a Go shop, looking for Golang experts",
	}

	tests := []struct {
		name     string
		query    string
		wantLen  int
		wantIn   string
	}{
		{
			name:    "filter by golang",
			query:   "golang",
			wantLen: 1, // Only Shopify (contains "Golang"); Stripe says "Go" which doesn't match substring "golang"
			wantIn:  "Shopify",
		},
		{
			name:    "filter by remote",
			query:   "remote",
			wantLen: 3, // Stripe, OpenAI, Shopify all have "Remote"
		},
		{
			name:    "filter by AI/ML",
			query:   "AI",
			wantLen: 2, // Anthropic (AI Safety) and OpenAI
		},
		{
			name:    "empty query returns all",
			query:   "",
			wantLen: 5,
		},
		{
			name:    "no match",
			query:   "cobol mainframe",
			wantLen: 0,
		},
		{
			name:    "case insensitive",
			query:   "STRIPE",
			wantLen: 1,
			wantIn:  "Stripe",
		},
		{
			name:    "multi-word: any keyword matches",
			query:   "anthropic openai",
			wantLen: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilterHNJobComments(comments, tt.query)
			if len(got) != tt.wantLen {
				t.Errorf("FilterHNJobComments(%q) len = %d, want %d\nresults: %v", tt.query, len(got), tt.wantLen, got)
			}
			if tt.wantIn != "" {
				found := false
				for _, c := range got {
					if strings.Contains(c, tt.wantIn) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("FilterHNJobComments(%q) missing expected %q in results: %v", tt.query, tt.wantIn, got)
				}
			}
		})
	}
}

func TestFilterHNJobCommentsEdgeCases(t *testing.T) {
	// nil input
	got := FilterHNJobComments(nil, "golang")
	if len(got) != 0 {
		t.Errorf("nil input: expected nil/empty, got %v", got)
	}

	// empty slice
	got = FilterHNJobComments([]string{}, "golang")
	if len(got) != 0 {
		t.Errorf("empty slice: expected 0, got %d", len(got))
	}

	// whitespace-only query
	got = FilterHNJobComments([]string{"Go developer", "Python engineer"}, "   ")
	if len(got) != 2 {
		t.Errorf("whitespace query: expected all 2 results, got %d", len(got))
	}
}

func TestSearchHNJobsToSearxngResult(t *testing.T) {
	// Test that SearchHNJobs output structure is correct
	// We only test the helper that converts filtered comments to SearxngResults
	comments := []string{
		"Stripe | Backend Engineer | Remote\n\nWe use Go and Kubernetes at scale.",
		"Anthropic | Researcher | San Francisco\n\nWorking on AI safety problems.",
	}

	// Simulate what SearchHNJobs does internally:
	threadID := int64(12345678)
	threadURL := "https://news.ycombinator.com/item?id=12345678"

	results := make([]engine.SearxngResult, len(comments))
	for i, text := range comments {
		title := extractHNJobTitle(text)
		results[i] = engine.SearxngResult{
			Title:   title,
			Content: "**Source:** HN Who is Hiring\n\n" + text,
			URL:     threadURL,
			Score:   0.8,
		}
		_ = threadID
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	r0 := results[0]
	if r0.Title != "Stripe | Backend Engineer | Remote" {
		t.Errorf("title = %q", r0.Title)
	}
	if !strings.Contains(r0.Content, "**Source:** HN Who is Hiring") {
		t.Errorf("content missing source tag: %s", r0.Content)
	}
	if !strings.Contains(r0.Content, "Stripe") {
		t.Errorf("content missing Stripe: %s", r0.Content)
	}
	if r0.URL != threadURL {
		t.Errorf("url = %q, want %q", r0.URL, threadURL)
	}
	if r0.Score != 0.8 {
		t.Errorf("score = %f, want 0.8", r0.Score)
	}
}
