package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// --- Greenhouse ---

const greenhouseBoardsAPI = "https://boards-api.greenhouse.io/v1/boards/%s/jobs"
const greenhouseSiteSearch = "site:boards.greenhouse.io"

// greenhouseSlugRe extracts company slug from boards.greenhouse.io URLs.
var greenhouseSlugRe = regexp.MustCompile(`boards\.greenhouse\.io/([^/?#]+)`)

// greenhouseJob is a single job from the Greenhouse public API.
type greenhouseJob struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	Location   struct {
		Name string `json:"name"`
	} `json:"location"`
	UpdatedAt   string `json:"updated_at"`
	AbsoluteURL string `json:"absolute_url"`
	Content     string `json:"content,omitempty"`
	Departments []struct {
		Name string `json:"name"`
	} `json:"departments,omitempty"`
}

type greenhouseResponse struct {
	Jobs []greenhouseJob `json:"jobs"`
}

// SearchGreenhouseJobs discovers company slugs via SearXNG then hits the public JSON API.
func SearchGreenhouseJobs(ctx context.Context, query, location string, limit int) ([]engine.SearxngResult, error) {
	engine.IncrGreenhouseRequests()

	searxQuery := query + " " + greenhouseSiteSearch
	if location != "" {
		searxQuery = query + " " + location + " " + greenhouseSiteSearch
	}

	searxResults, err := engine.SearchSearXNG(ctx, searxQuery, "all", "", "google")
	if err != nil {
		return nil, fmt.Errorf("greenhouse SearXNG: %w", err)
	}

	// Extract unique company slugs from result URLs.
	slugs := extractGreenhouseSlugs(searxResults)
	if len(slugs) == 0 {
		slog.Debug("greenhouse: no slugs found in SearXNG results")
		return nil, nil
	}
	if len(slugs) > 5 {
		slugs = slugs[:5]
	}

	// Fetch jobs from each company's public API in parallel.
	type fetchResult struct {
		slug string
		jobs []greenhouseJob
		err  error
	}
	ch := make(chan fetchResult, len(slugs))
	for _, slug := range slugs {
		go func(s string) {
			jobs, err := fetchGreenhouseJobs(ctx, s)
			ch <- fetchResult{s, jobs, err}
		}(slug)
	}

	keywords := strings.Fields(strings.ToLower(query))
	var allResults []engine.SearxngResult
	for i := 0; i < len(slugs); i++ {
		r := <-ch
		if r.err != nil {
			slog.Debug("greenhouse: fetch error", slog.String("slug", r.slug), slog.Any("error", r.err))
			continue
		}
		for _, job := range r.jobs {
			if !matchesKeywords(job.Title+" "+job.Location.Name, keywords) {
				continue
			}
			jobURL := job.AbsoluteURL
			if jobURL == "" {
				jobURL = fmt.Sprintf("https://boards.greenhouse.io/%s/jobs/%d", r.slug, job.ID)
			}
			content := fmt.Sprintf("**Source:** Greenhouse | **Company:** %s | **Location:** %s", r.slug, job.Location.Name)
			if len(job.Departments) > 0 {
				content += " | **Dept:** " + job.Departments[0].Name
			}
			if job.UpdatedAt != "" && len(job.UpdatedAt) >= 10 {
				content += " | **Updated:** " + job.UpdatedAt[:10]
			}
			if job.Content != "" {
				desc := engine.TruncateRunes(engine.CleanHTML(job.Content), 600, "...")
				content += "\n\n" + desc
			}
			allResults = append(allResults, engine.SearxngResult{
				Title:   job.Title,
				Content: content,
				URL:     jobURL,
				Score:   0.9,
			})
			if len(allResults) >= limit {
				break
			}
		}
		if len(allResults) >= limit {
			break
		}
	}

	slog.Debug("greenhouse: search complete", slog.Int("results", len(allResults)))
	return allResults, nil
}

// fetchGreenhouseJobs fetches all jobs for a given company slug.
func fetchGreenhouseJobs(ctx context.Context, slug string) ([]greenhouseJob, error) {
	apiURL := fmt.Sprintf(greenhouseBoardsAPI, slug)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentBot)
	req.Header.Set("Accept", "application/json")

	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		return engine.Cfg.HTTPClient.Do(req)
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("greenhouse API status %d for %s", resp.StatusCode, slug)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, err
	}

	var gr greenhouseResponse
	if err := json.Unmarshal(body, &gr); err != nil {
		return nil, fmt.Errorf("greenhouse parse: %w", err)
	}
	return gr.Jobs, nil
}

// extractGreenhouseSlugs extracts unique company slugs from SearXNG result URLs.
func extractGreenhouseSlugs(results []engine.SearxngResult) []string {
	seen := make(map[string]bool)
	var slugs []string
	for _, r := range results {
		if m := greenhouseSlugRe.FindStringSubmatch(r.URL); m != nil {
			slug := strings.ToLower(m[1])
			if slug != "" && !seen[slug] {
				seen[slug] = true
				slugs = append(slugs, slug)
			}
		}
	}
	return slugs
}

// --- Lever ---

const leverAPIBase = "https://api.lever.co/v0/postings/%s?mode=json"
const leverSiteSearch = "site:jobs.lever.co"

// leverSlugRe extracts company slug from jobs.lever.co URLs.
var leverSlugRe = regexp.MustCompile(`jobs\.lever\.co/([^/?#]+)`)

// leverPosting is a single job from the Lever public API.
type leverPosting struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	HostedURL string `json:"hostedUrl"`
	ApplyURL  string `json:"applyUrl"`
	Categories struct {
		Location     string   `json:"location"`
		AllLocations []string `json:"allLocations"`
		Team         string   `json:"team"`
		Commitment   string   `json:"commitment"`
		Department   string   `json:"department"`
	} `json:"categories"`
	SalaryRange struct {
		Min      int    `json:"min"`
		Max      int    `json:"max"`
		Currency string `json:"currency"`
	} `json:"salaryRange"`
	CreatedAt        int64  `json:"createdAt"`
	DescriptionPlain string `json:"descriptionPlain"`
	WorkplaceType    string `json:"workplaceType"`
	Country          string `json:"country"`
}

// SearchLeverJobs discovers company slugs via SearXNG then hits the public JSON API.
func SearchLeverJobs(ctx context.Context, query, location string, limit int) ([]engine.SearxngResult, error) {
	engine.IncrLeverRequests()

	searxQuery := query + " " + leverSiteSearch
	if location != "" {
		searxQuery = query + " " + location + " " + leverSiteSearch
	}

	searxResults, err := engine.SearchSearXNG(ctx, searxQuery, "all", "", "google")
	if err != nil {
		return nil, fmt.Errorf("lever SearXNG: %w", err)
	}

	slugs := extractLeverSlugs(searxResults)
	if len(slugs) == 0 {
		slog.Debug("lever: no slugs found in SearXNG results")
		return nil, nil
	}
	if len(slugs) > 5 {
		slugs = slugs[:5]
	}

	type fetchResult struct {
		slug     string
		postings []leverPosting
		err      error
	}
	ch := make(chan fetchResult, len(slugs))
	for _, slug := range slugs {
		go func(s string) {
			postings, err := fetchLeverPostings(ctx, s)
			ch <- fetchResult{s, postings, err}
		}(slug)
	}

	keywords := strings.Fields(strings.ToLower(query))
	var allResults []engine.SearxngResult
	for i := 0; i < len(slugs); i++ {
		r := <-ch
		if r.err != nil {
			slog.Debug("lever: fetch error", slog.String("slug", r.slug), slog.Any("error", r.err))
			continue
		}
		for _, p := range r.postings {
			haystack := p.Text + " " + p.Categories.Location + " " + p.Categories.Team
			if !matchesKeywords(haystack, keywords) {
				continue
			}
			jobURL := p.HostedURL
			if jobURL == "" {
				jobURL = fmt.Sprintf("https://jobs.lever.co/%s/%s", r.slug, p.ID)
			}
			loc := p.Categories.Location
			if loc == "" && len(p.Categories.AllLocations) > 0 {
				loc = strings.Join(p.Categories.AllLocations, ", ")
			}
			content := fmt.Sprintf("**Source:** Lever | **Company:** %s | **Location:** %s", r.slug, loc)
			if p.Categories.Team != "" {
				content += " | **Team:** " + p.Categories.Team
			}
			if p.Categories.Commitment != "" {
				content += " | **Type:** " + p.Categories.Commitment
			}
			if p.WorkplaceType != "" {
				content += " | **Remote:** " + p.WorkplaceType
			}
			if p.SalaryRange.Min > 0 {
				if p.SalaryRange.Max > p.SalaryRange.Min {
					content += fmt.Sprintf(" | **Salary:** $%d-$%d %s", p.SalaryRange.Min, p.SalaryRange.Max, p.SalaryRange.Currency)
				} else {
					content += fmt.Sprintf(" | **Salary:** $%d %s", p.SalaryRange.Min, p.SalaryRange.Currency)
				}
			}
			if p.DescriptionPlain != "" {
				desc := engine.TruncateRunes(p.DescriptionPlain, 600, "...")
				content += "\n\n" + desc
			}
			allResults = append(allResults, engine.SearxngResult{
				Title:   p.Text,
				Content: content,
				URL:     jobURL,
				Score:   0.9,
			})
			if len(allResults) >= limit {
				break
			}
		}
		if len(allResults) >= limit {
			break
		}
	}

	slog.Debug("lever: search complete", slog.Int("results", len(allResults)))
	return allResults, nil
}

// fetchLeverPostings fetches all job postings for a given company slug.
func fetchLeverPostings(ctx context.Context, slug string) ([]leverPosting, error) {
	apiURL := fmt.Sprintf(leverAPIBase, slug)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentBot)
	req.Header.Set("Accept", "application/json")

	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		return engine.Cfg.HTTPClient.Do(req)
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lever API status %d for %s", resp.StatusCode, slug)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, err
	}

	var postings []leverPosting
	if err := json.Unmarshal(body, &postings); err != nil {
		return nil, fmt.Errorf("lever parse: %w", err)
	}
	return postings, nil
}

// extractLeverSlugs extracts unique company slugs from SearXNG result URLs.
func extractLeverSlugs(results []engine.SearxngResult) []string {
	seen := make(map[string]bool)
	var slugs []string
	for _, r := range results {
		if m := leverSlugRe.FindStringSubmatch(r.URL); m != nil {
			slug := strings.ToLower(m[1])
			if slug != "" && !seen[slug] {
				seen[slug] = true
				slugs = append(slugs, slug)
			}
		}
	}
	return slugs
}

// --- Shared helpers ---

// matchesKeywords returns true if haystack contains any of the keywords (case-insensitive).
func matchesKeywords(haystack string, keywords []string) bool {
	if len(keywords) == 0 {
		return true
	}
	lower := strings.ToLower(haystack)
	for _, kw := range keywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// extractATSSlug is a helper used by tool layer to pull company name from ATS URLs.
func extractATSCompanyName(rawURL string) string {
	if m := greenhouseSlugRe.FindStringSubmatch(rawURL); m != nil {
		return m[1]
	}
	if m := leverSlugRe.FindStringSubmatch(rawURL); m != nil {
		return m[1]
	}
	u, err := url.Parse(rawURL)
	if err == nil {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) > 0 && parts[0] != "" {
			return parts[0]
		}
	}
	return ""
}
