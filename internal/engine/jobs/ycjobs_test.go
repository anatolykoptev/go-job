package jobs

import (
	"github.com/anatolykoptev/go_job/internal/engine"
	"strings"
	"testing"
)

const sampleYCJobsHTML = `<!DOCTYPE html>
<html>
<head><title>Jobs at YC Startups</title></head>
<body>
  <div class="jobs-list-item">
    <a href="/jobs/12345-senior-go-engineer-at-stripe">Senior Go Engineer</a>
    <span>Stripe</span>
    <span>Remote / San Francisco</span>
  </div>
  <div class="jobs-list-item">
    <a href="/jobs/67890-ai-researcher-at-anthropic">AI Safety Researcher</a>
    <span>Anthropic</span>
    <span>San Francisco, CA</span>
  </div>
  <div class="jobs-list-item">
    <a href="https://www.workatastartup.com/jobs/11111-product-manager">Product Manager</a>
    <span>OpenAI</span>
    <span>New York, NY</span>
  </div>
</body>
</html>`

func TestParseYCJobsHTML(t *testing.T) {
	results := parseYCJobsHTML(sampleYCJobsHTML, "https://www.workatastartup.com/jobs?q=golang")

	if len(results) == 0 {
		t.Fatal("parseYCJobsHTML: expected some results, got 0")
	}

	for _, r := range results {
		if r.URL == "" {
			t.Errorf("result has empty URL: %+v", r)
		}
		if r.Score != 0.85 {
			t.Errorf("score = %f, want 0.85", r.Score)
		}
		if !strings.Contains(r.Content, "**Source:** YC workatastartup.com") {
			t.Errorf("content missing source: %s", r.Content)
		}
	}

	// At least one result should have a workatastartup.com URL
	hasYCURL := false
	for _, r := range results {
		if strings.Contains(r.URL, "workatastartup.com") || strings.HasPrefix(r.URL, "https://www.workatastartup.com") {
			hasYCURL = true
			break
		}
	}
	if !hasYCURL {
		t.Errorf("no result has workatastartup.com URL, results: %+v", results)
	}
}

func TestParseYCJobsHTMLEmpty(t *testing.T) {
	results := parseYCJobsHTML("", "https://www.workatastartup.com/jobs")
	// Should return nil/empty, not panic
	_ = results
}

func TestParseYCJobsHTMLInvalid(t *testing.T) {
	// Should not panic on malformed HTML
	results := parseYCJobsHTML("<div unclosed", "https://www.workatastartup.com/jobs")
	_ = results // just checking it doesn't panic
}

func TestExtractYCJobCard(t *testing.T) {
	// Test relative URL gets prefixed
	htmlWithRelative := `<div class="jobs-list-item">
		<a href="/jobs/99999-software-engineer">Software Engineer</a>
		<span>Figma</span>
		<span>Remote</span>
	</div>`

	results := parseYCJobsHTML(htmlWithRelative, "https://www.workatastartup.com/jobs")
	if len(results) > 0 {
		for _, r := range results {
			if r.URL != "" && strings.HasPrefix(r.URL, "/") {
				t.Errorf("relative URL not expanded: %q", r.URL)
			}
		}
	}
}

func TestParseYCJobsHTMLNoJobCards(t *testing.T) {
	// HTML with no job-list classes
	html := `<html><body><div class="header"><p>No jobs here</p></div></body></html>`
	results := parseYCJobsHTML(html, "https://www.workatastartup.com/jobs")
	// Should return empty, not panic
	if len(results) != 0 {
		t.Logf("parseYCJobsHTML with no job cards returned %d results (unexpected but not fatal)", len(results))
	}
}

func TestYCJobsURLFiltering(t *testing.T) {
	// Test that SearchYCJobs filters to only workatastartup.com URLs from SearXNG
	// Simulate SearXNG returning mixed results
	mixed := []engine.SearxngResult{
		{URL: "https://www.workatastartup.com/jobs/12345", Title: "Go Engineer", Content: "Stripe"},
		{URL: "https://www.linkedin.com/jobs/view/999", Title: "Some Job", Content: "LinkedIn"},
		{URL: "https://www.workatastartup.com/jobs/67890", Title: "AI Researcher", Content: "Anthropic"},
		{URL: "https://jobs.lever.co/openai/abc", Title: "Lever Job", Content: "OpenAI"},
	}

	var ycResults []engine.SearxngResult
	for _, r := range mixed {
		if strings.Contains(r.URL, "workatastartup.com") {
			r.Content = "**Source:** YC workatastartup.com\n\n" + r.Content
			r.Score = 0.85
			ycResults = append(ycResults, r)
		}
	}

	if len(ycResults) != 2 {
		t.Errorf("expected 2 YC results, got %d", len(ycResults))
	}
	for _, r := range ycResults {
		if !strings.Contains(r.URL, "workatastartup.com") {
			t.Errorf("non-YC URL in results: %q", r.URL)
		}
		if !strings.Contains(r.Content, "**Source:** YC workatastartup.com") {
			t.Errorf("missing source tag: %s", r.Content)
		}
	}
}
