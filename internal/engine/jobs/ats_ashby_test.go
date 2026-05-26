package jobs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// --- extractAshbySlugs ---

func TestExtractAshbySlugs(t *testing.T) {
	tests := []struct {
		name    string
		results []engine.SearxngResult
		want    []string
	}{
		{
			name: "standard ashby URLs",
			results: []engine.SearxngResult{
				{URL: "https://jobs.ashbyhq.com/modal/abc-def-123"},
				{URL: "https://jobs.ashbyhq.com/cursor/xyz-789"},
			},
			want: []string{"modal", "cursor"},
		},
		{
			name: "dedup same slug",
			results: []engine.SearxngResult{
				{URL: "https://jobs.ashbyhq.com/cognition/job1"},
				{URL: "https://jobs.ashbyhq.com/cognition/job2"},
				{URL: "https://jobs.ashbyhq.com/replit/job3"},
			},
			want: []string{"cognition", "replit"},
		},
		{
			name: "non-ashby URLs ignored",
			results: []engine.SearxngResult{
				{URL: "https://boards.greenhouse.io/stripe/jobs/1"},
				{URL: "https://jobs.lever.co/notion/abc"},
				{URL: "https://jobs.ashbyhq.com/perplexity/uuid"},
			},
			want: []string{"perplexity"},
		},
		{
			name: "slug normalized to lowercase",
			results: []engine.SearxngResult{
				{URL: "https://jobs.ashbyhq.com/Modal/abc"},
			},
			want: []string{"modal"},
		},
		{
			name:    "empty input",
			results: []engine.SearxngResult{},
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractAshbySlugs(tt.results)
			if len(got) != len(tt.want) {
				t.Fatalf("extractAshbySlugs() = %v, want %v", got, tt.want)
			}
			for i, s := range got {
				if s != tt.want[i] {
					t.Errorf("[%d] = %q, want %q", i, s, tt.want[i])
				}
			}
		})
	}
}

// --- buildAshbyLocation ---

func TestBuildAshbyLocation(t *testing.T) {
	tests := []struct {
		name string
		job  ashbyJob
		want string
	}{
		{
			name: "location only",
			job:  ashbyJob{Location: "San Francisco, CA"},
			want: "San Francisco, CA",
		},
		{
			name: "remote flagged",
			job:  ashbyJob{Location: "San Francisco, CA", IsRemote: true},
			want: "San Francisco, CA | Remote",
		},
		{
			name: "remote no primary location",
			job:  ashbyJob{IsRemote: true},
			want: "Remote",
		},
		{
			name: "secondary locations appended",
			job: ashbyJob{
				Location: "New York",
				SecondaryLocations: []struct {
					Location string `json:"location"`
				}{
					{Location: "Austin"},
					{Location: "Seattle"},
				},
			},
			want: "New York (+Austin, Seattle)",
		},
		{
			name: "empty secondary location entries skipped",
			job: ashbyJob{
				Location: "Chicago",
				SecondaryLocations: []struct {
					Location string `json:"location"`
				}{
					{Location: ""},
					{Location: "Denver"},
				},
			},
			want: "Chicago (+Denver)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildAshbyLocation(tt.job)
			if got != tt.want {
				t.Errorf("buildAshbyLocation() = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- fetchAshbyJobs via httptest ---

// newAshbyTestServer returns a test server that responds to the Ashby board API format.
// It serves /<slug> with the given jobs list; any other path returns 404.
func newAshbyTestServer(t *testing.T, slug string, jobsList []ashbyJob) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, slug) {
			http.NotFound(w, r)
			return
		}
		resp := ashbyResponse{Jobs: jobsList}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("encode error: %v", err)
		}
	}))
}

// patchAshbyAPI replaces ashbyBoardAPI with a test-server URL template and restores
// the original on test cleanup.
func patchAshbyAPI(t *testing.T, serverURL string) {
	t.Helper()
	orig := ashbyBoardAPI
	ashbyBoardAPI = serverURL + "/posting-api/job-board/%s?includeCompensation=true"
	t.Cleanup(func() { ashbyBoardAPI = orig })
}

func TestFetchAshbyJobs_Success(t *testing.T) {
	want := []ashbyJob{
		{
			ID:               "uuid-1",
			Title:            "Staff Engineer",
			Location:         "Remote",
			IsRemote:         true,
			WorkplaceType:    "Remote",
			JobURL:           "https://jobs.ashbyhq.com/modal/uuid-1",
			DescriptionPlain: "Build infrastructure.",
			PublishedAt:      "2026-05-01T00:00:00Z",
		},
	}

	srv := newAshbyTestServer(t, "modal", want)
	defer srv.Close()
	patchAshbyAPI(t, srv.URL)

	ctx := context.Background()
	got, err := fetchAshbyJobs(ctx, "modal")
	if err != nil {
		t.Fatalf("fetchAshbyJobs error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 job, got %d", len(got))
	}
	j := got[0]
	if j.ID != "uuid-1" {
		t.Errorf("id = %q, want 'uuid-1'", j.ID)
	}
	if j.Title != "Staff Engineer" {
		t.Errorf("title = %q, want 'Staff Engineer'", j.Title)
	}
	if !j.IsRemote {
		t.Error("expected IsRemote=true")
	}
	if j.DescriptionPlain != "Build infrastructure." {
		t.Errorf("description = %q", j.DescriptionPlain)
	}
}

func TestFetchAshbyJobs_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	patchAshbyAPI(t, srv.URL)

	ctx := context.Background()
	got, err := fetchAshbyJobs(ctx, "nonexistent-slug")
	if err != nil {
		t.Fatalf("expected nil error on 404, got: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil jobs on 404, got %v", got)
	}
}

func TestFetchAshbyJobs_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{not valid json`))
	}))
	defer srv.Close()
	patchAshbyAPI(t, srv.URL)

	ctx := context.Background()
	_, err := fetchAshbyJobs(ctx, "any-slug")
	if err == nil {
		t.Fatal("expected error on invalid JSON, got nil")
	}
}

func TestFetchAshbyJobs_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer srv.Close()
	patchAshbyAPI(t, srv.URL)

	ctx := context.Background()
	_, err := fetchAshbyJobs(ctx, "any-slug")
	if err == nil {
		t.Fatal("expected error on 500 response, got nil")
	}
}

// --- Ashby result content format ---

func TestAshbyResultContent(t *testing.T) {
	slug := "cognition"
	j := ashbyJob{
		ID:            "job-uuid-123",
		Title:         "Principal Engineer",
		Location:      "San Francisco",
		IsRemote:      false,
		WorkplaceType: "OnSite",
		JobURL:        "https://jobs.ashbyhq.com/cognition/job-uuid-123",
		Department:    "Engineering",
	}
	j.Compensation.CompensationTierSummary = "$280K – $400K"
	j.PublishedAt = "2026-04-15T00:00:00Z"

	loc := buildAshbyLocation(j)
	content := "**Source:** Ashby | **Company:** " + slug + " | **Location:** " + loc
	content += " | **Type:** " + j.WorkplaceType
	content += " | **Dept:** " + j.Department
	content += " | **Comp:** " + j.Compensation.CompensationTierSummary
	if len(j.PublishedAt) >= 10 {
		content += " | **Published:** " + j.PublishedAt[:10]
	}

	result := engine.SearxngResult{
		Title:   j.Title,
		Content: content,
		URL:     j.JobURL,
		Score:   0.9,
	}

	if result.Title != "Principal Engineer" {
		t.Errorf("title = %q", result.Title)
	}
	if !strings.Contains(result.Content, "**Source:** Ashby") {
		t.Errorf("missing source: %s", result.Content)
	}
	if !strings.Contains(result.Content, "**Company:** cognition") {
		t.Errorf("missing company: %s", result.Content)
	}
	if !strings.Contains(result.Content, "$280K") {
		t.Errorf("missing comp: %s", result.Content)
	}
	if !strings.Contains(result.Content, "2026-04-15") {
		t.Errorf("missing published date: %s", result.Content)
	}
	if result.Score != 0.9 {
		t.Errorf("score = %f, want 0.9", result.Score)
	}
}

// TestExtractATSCompanyName_Ashby verifies extractATSCompanyName handles Ashby URLs.
// Note: extractATSCompanyName returns the raw captured slug (no lowercasing)
// — lowercasing is done in the slug-extraction helpers for the search flow.
func TestExtractATSCompanyName_Ashby(t *testing.T) {
	tests := []struct {
		rawURL string
		want   string
	}{
		{"https://jobs.ashbyhq.com/modal/some-uuid", "modal"},
		{"https://jobs.ashbyhq.com/Cursor/job-id", "Cursor"}, // raw slug, not lowercased
		{"https://jobs.ashbyhq.com/perplexity", "perplexity"},
	}
	for _, tt := range tests {
		got := extractATSCompanyName(tt.rawURL)
		if got != tt.want {
			t.Errorf("extractATSCompanyName(%q) = %q, want %q", tt.rawURL, got, tt.want)
		}
	}
}
