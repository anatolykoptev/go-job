//go:build integration

package jobs

import (
	"github.com/anatolykoptev/go_job/internal/engine"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func initTestEngine() {
	Init(Config{
		SearxngURL:      "http://127.0.0.1:8888",
		MaxContentChars: 4000,
		FetchTimeout:    15 * time.Second,
		HTTPClient: &http.Client{
			Timeout: 20 * time.Second,
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 5,
				IdleConnTimeout:     30 * time.Second,
			},
		},
	})
	InitCache("", 15*time.Minute, 100, 5*time.Minute)
}

// --- HN Who is Hiring ---

func TestIntegration_FindWhoIsHiringThread(t *testing.T) {
	initTestEngine()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	threadID, err := FindWhoIsHiringThread(ctx)
	if err != nil {
		t.Fatalf("FindWhoIsHiringThread error: %v", err)
	}
	if threadID <= 0 {
		t.Errorf("invalid thread ID: %d", threadID)
	}
	t.Logf("✓ Found Who is Hiring thread: %d (https://news.ycombinator.com/item?id=%d)", threadID, threadID)
}

func TestIntegration_FetchHNJobComments(t *testing.T) {
	initTestEngine()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	threadID, err := FindWhoIsHiringThread(ctx)
	if err != nil {
		t.Fatalf("FindWhoIsHiringThread error: %v", err)
	}

	comments, err := FetchHNJobComments(ctx, threadID, 10)
	if err != nil {
		t.Fatalf("FetchHNJobComments error: %v", err)
	}
	if len(comments) == 0 {
		t.Error("expected at least 1 comment, got 0")
	}
	t.Logf("✓ Fetched %d job comments from thread %d", len(comments), threadID)
	for i, c := range comments[:min(3, len(comments))] {
		preview := c
		if len(preview) > 120 {
			preview = preview[:120] + "..."
		}
		t.Logf("  [%d] %s", i+1, preview)
	}
}

func TestIntegration_SearchHNJobs_Golang(t *testing.T) {
	initTestEngine()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	results, err := SearchHNJobs(ctx, "golang", 10)
	if err != nil {
		t.Fatalf("SearchHNJobs error: %v", err)
	}
	t.Logf("✓ SearchHNJobs('golang'): %d results", len(results))
	for i, r := range results[:min(3, len(results))] {
		t.Logf("  [%d] %s | %s", i+1, r.Title, r.URL)
	}
}

func TestIntegration_SearchHNJobs_Remote(t *testing.T) {
	initTestEngine()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	results, err := SearchHNJobs(ctx, "remote python", 15)
	if err != nil {
		t.Fatalf("SearchHNJobs error: %v", err)
	}
	t.Logf("✓ SearchHNJobs('remote python'): %d results", len(results))
	for _, r := range results[:min(3, len(results))] {
		if !strings.Contains(strings.ToLower(r.Content), "python") &&
			!strings.Contains(strings.ToLower(r.Content), "remote") {
			t.Logf("  WARN: result may not match query: %s", r.Title)
		}
	}
}

// --- Greenhouse API ---

func TestIntegration_FetchGreenhouseJobs_Stripe(t *testing.T) {
	initTestEngine()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	jobs, err := fetchGreenhouseJobs(ctx, "stripe")
	if err != nil {
		t.Fatalf("fetchGreenhouseJobs(stripe) error: %v", err)
	}
	if len(jobs) == 0 {
		t.Error("expected >0 jobs from Stripe Greenhouse, got 0")
	}
	t.Logf("✓ Greenhouse Stripe: %d jobs", len(jobs))
	for _, j := range jobs[:min(3, len(jobs))] {
		t.Logf("  - %s | %s | %s", j.Title, j.Location.Name, j.AbsoluteURL)
	}
}

func TestIntegration_FetchGreenhouseJobs_Anthropic(t *testing.T) {
	initTestEngine()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	jobs, err := fetchGreenhouseJobs(ctx, "anthropic")
	if err != nil {
		t.Fatalf("fetchGreenhouseJobs(anthropic) error: %v", err)
	}
	if len(jobs) == 0 {
		t.Error("expected >0 jobs from Anthropic Greenhouse, got 0")
	}
	t.Logf("✓ Greenhouse Anthropic: %d jobs", len(jobs))
	for _, j := range jobs[:min(3, len(jobs))] {
		t.Logf("  - %s | %s", j.Title, j.Location.Name)
	}
}

func TestIntegration_FetchGreenhouseJobs_NotFound(t *testing.T) {
	initTestEngine()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Non-existent company should return nil, not error (404 handled gracefully)
	jobs, err := fetchGreenhouseJobs(ctx, "this-company-does-not-exist-xyz-999")
	if err != nil {
		t.Errorf("expected nil error for 404, got: %v", err)
	}
	if len(jobs) != 0 {
		t.Errorf("expected 0 jobs for non-existent company, got %d", len(jobs))
	}
	t.Logf("✓ Greenhouse 404 handled gracefully")
}

func TestIntegration_SearchGreenhouseJobs(t *testing.T) {
	initTestEngine()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// This uses SearXNG to find slugs first, so SearXNG must be running.
	results, err := SearchGreenhouseJobs(ctx, "software engineer golang", "", 10)
	if err != nil {
		t.Logf("SearchGreenhouseJobs error (SearXNG may not be available): %v", err)
		t.Skip("SearXNG not available for slug discovery")
	}
	t.Logf("✓ SearchGreenhouseJobs('software engineer golang'): %d results", len(results))
	for _, r := range results[:min(3, len(results))] {
		if !strings.Contains(r.Content, "**Source:** Greenhouse") {
			t.Errorf("result missing Greenhouse source tag: %s", r.Content[:min(100, len(r.Content))])
		}
		t.Logf("  - %s | %s", r.Title, r.URL)
	}
}

// --- Lever API ---

func TestIntegration_FetchLeverPostings_Plaid(t *testing.T) {
	initTestEngine()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Plaid confirmed to have 94 jobs on Lever public API.
	postings, err := fetchLeverPostings(ctx, "plaid")
	if err != nil {
		t.Fatalf("fetchLeverPostings(plaid) error: %v", err)
	}
	if len(postings) == 0 {
		t.Error("expected >0 postings from Plaid Lever, got 0")
	}
	t.Logf("✓ Lever Plaid: %d postings", len(postings))
	for _, p := range postings[:min(3, len(postings))] {
		loc := p.Categories.Location
		if loc == "" && len(p.Categories.AllLocations) > 0 {
			loc = p.Categories.AllLocations[0]
		}
		salary := ""
		if p.SalaryRange.Min > 0 {
			salary = fmt.Sprintf(" | $%d-%d", p.SalaryRange.Min, p.SalaryRange.Max)
		}
		t.Logf("  - %s | %s%s | %s", p.Text, loc, salary, p.HostedURL)
	}

	// Verify struct fields are properly parsed
	p0 := postings[0]
	if p0.Text == "" {
		t.Error("first posting has empty Text")
	}
	if p0.HostedURL == "" && p0.ID == "" {
		t.Error("first posting has no URL or ID")
	}
}

func TestIntegration_FetchLeverPostings_NotFound(t *testing.T) {
	initTestEngine()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	postings, err := fetchLeverPostings(ctx, "this-company-does-not-exist-xyz-999")
	if err != nil {
		t.Errorf("expected nil error for 404, got: %v", err)
	}
	if len(postings) != 0 {
		t.Errorf("expected 0 postings for non-existent company, got %d", len(postings))
	}
	t.Logf("✓ Lever 404 handled gracefully")
}

func TestIntegration_SearchLeverJobs(t *testing.T) {
	initTestEngine()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := SearchLeverJobs(ctx, "product designer", "", 10)
	if err != nil {
		t.Logf("SearchLeverJobs error (SearXNG may not be available): %v", err)
		t.Skip("SearXNG not available for slug discovery")
	}
	t.Logf("✓ SearchLeverJobs('product designer'): %d results", len(results))
	for _, r := range results[:min(3, len(results))] {
		if !strings.Contains(r.Content, "**Source:** Lever") {
			t.Errorf("result missing Lever source tag: %s", r.Content[:min(100, len(r.Content))])
		}
		t.Logf("  - %s | %s", r.Title, r.URL)
	}
}

// --- YC workatastartup ---

func TestIntegration_SearchYCJobs(t *testing.T) {
	initTestEngine()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := SearchYCJobs(ctx, "software engineer", "remote", 10)
	if err != nil {
		t.Logf("SearchYCJobs error (SearXNG may not be available): %v", err)
		t.Skip("SearXNG not available")
	}
	t.Logf("✓ SearchYCJobs('software engineer', 'remote'): %d results", len(results))
	for _, r := range results[:min(3, len(results))] {
		if !strings.Contains(r.URL, "workatastartup.com") {
			t.Errorf("YC result has non-YC URL: %s", r.URL)
		}
		t.Logf("  - %s | %s", r.Title, r.URL)
	}
}

// --- Helpers ---

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestIntegration_Summary(t *testing.T) {
	t.Log("")
	t.Log("Integration test summary:")
	t.Log("  HN jobs: FindWhoIsHiringThread → FetchHNJobComments → FilterHNJobComments")
	t.Log("  Greenhouse: fetchGreenhouseJobs (stripe, anthropic) + 404 handling")
	t.Log("  Lever: fetchLeverPostings (figma) + 404 handling")
	t.Log("  YC: SearchYCJobs (SearXNG-based)")
	t.Log(fmt.Sprintf("  Test time: %s", time.Now().Format("2006-01-02 15:04:05")))
}
