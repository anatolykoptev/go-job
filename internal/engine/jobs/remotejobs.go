package jobs

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
)

const remoteOKAPI = "https://remoteok.com/api"
const wwrRSSURL = "https://weworkremotely.com/remote-jobs.rss"

// --- RemoteOK API types ---

type remoteOKJob struct {
	Slug      string   `json:"slug"`
	ID        string   `json:"id"`
	Position  string   `json:"position"`
	Company   string   `json:"company"`
	Tags      []string `json:"tags"`
	Location  string   `json:"location"`
	SalaryMin int      `json:"salary_min"`
	SalaryMax int      `json:"salary_max"`
	Date      string   `json:"date"`
	URL       string   `json:"url"`
}

// --- WeWorkRemotely RSS types ---

type wwrRSS struct {
	XMLName xml.Name   `xml:"rss"`
	Channel wwrChannel `xml:"channel"`
}

type wwrChannel struct {
	Items []wwrItem `xml:"item"`
}

type wwrItem struct {
	Title    string `xml:"title"`
	Link     string `xml:"link"`
	PubDate  string `xml:"pubDate"`
	Category string `xml:"category"`
	Type     string `xml:"type"`
	Region   string `xml:"region"`
	Skills   string `xml:"skills"`
}

// SearchRemoteOK queries the RemoteOK JSON API for remote job listings.
func SearchRemoteOK(ctx context.Context, query string, limit int) ([]engine.RemoteJobListing, error) {
	engine.IncrRemoteOKRequests()

	if limit <= 0 || limit > 30 {
		limit = 20
	}

	// Extract best tag from query: prefer tech/role keywords over generic words.
	fields := strings.Fields(strings.ToLower(query))
	if len(fields) == 0 {
		return nil, fmt.Errorf("query cannot be empty")
	}
	tag := pickBestRemoteOKTag(fields)

	u, err := url.Parse(remoteOKAPI)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("tag", tag)
	u.RawQuery = q.Encode()
	apiURL := u.String()

	ctx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
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

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("RemoteOK API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, err
	}

	jobs, err := parseRemoteOKResponse(body)
	if err != nil {
		return nil, err
	}

	// Filter by keyword match (query words in position + tags + company).
	filtered := filterRemoteJobs(jobs, query)

	if len(filtered) > limit {
		filtered = filtered[:limit]
	}

	slog.Debug("remoteok: search complete", slog.String("tag", tag), slog.Int("raw", len(jobs)), slog.Int("filtered", len(filtered)))
	return filtered, nil
}

// parseRemoteOKResponse parses the RemoteOK JSON array, skipping [0] (metadata).
func parseRemoteOKResponse(body []byte) ([]engine.RemoteJobListing, error) {
	var raw []json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("remoteok parse error: %w", err)
	}

	// Skip first element (metadata with "legal" field).
	if len(raw) <= 1 {
		return nil, nil
	}

	var jobs []engine.RemoteJobListing
	for _, item := range raw[1:] {
		var j remoteOKJob
		if err := json.Unmarshal(item, &j); err != nil {
			continue
		}
		if j.Position == "" {
			continue
		}

		salary := formatRemoteSalary(j.SalaryMin, j.SalaryMax)

		jobURL := j.URL
		if jobURL == "" && j.Slug != "" {
			jobURL = "https://remoteok.com/remote-jobs/" + j.Slug
		}

		posted := ""
		if j.Date != "" {
			if t, err := time.Parse(time.RFC3339, j.Date); err == nil {
				posted = t.UTC().Format("2006-01-02")
			} else {
				posted = j.Date
			}
		}

		tags := make([]string, len(j.Tags))
		copy(tags, j.Tags)

		jobs = append(jobs, engine.RemoteJobListing{
			Title:    j.Position,
			Company:  j.Company,
			URL:      jobURL,
			Source:   "remoteok",
			Salary:   salary,
			Location: j.Location,
			Tags:     tags,
			Posted:   posted,
			JobType:  "remote",
		})
	}

	return jobs, nil
}

// formatRemoteSalary formats salary range from RemoteOK min/max values.
func formatRemoteSalary(min, max int) string {
	if min == 0 && max == 0 {
		return "not specified"
	}
	if min == max {
		return fmt.Sprintf("$%d", max)
	}
	return fmt.Sprintf("$%d - $%d", min, max)
}

// SearchWeWorkRemotely fetches and parses the WWR RSS feed.
func SearchWeWorkRemotely(ctx context.Context, query string, limit int) ([]engine.RemoteJobListing, error) {
	engine.IncrWWRRequests()

	if limit <= 0 || limit > 30 {
		limit = 20
	}

	ctx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", wwrRSSURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentBot)
	req.Header.Set("Accept", "application/xml, application/rss+xml")

	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		return engine.Cfg.HTTPClient.Do(req)
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("WWR RSS returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, err
	}

	jobs, err := parseWWRResponse(body)
	if err != nil {
		return nil, err
	}

	// Filter by keyword match since RSS returns all listings.
	filtered := filterRemoteJobs(jobs, query)

	if len(filtered) > limit {
		filtered = filtered[:limit]
	}

	slog.Debug("wwr: search complete", slog.Int("raw", len(jobs)), slog.Int("filtered", len(filtered)))
	return filtered, nil
}

// parseWWRResponse parses the WeWorkRemotely RSS XML feed.
func parseWWRResponse(body []byte) ([]engine.RemoteJobListing, error) {
	var rss wwrRSS
	if err := xml.Unmarshal(body, &rss); err != nil {
		return nil, fmt.Errorf("wwr rss parse error: %w", err)
	}

	var jobs []engine.RemoteJobListing
	for _, item := range rss.Channel.Items {
		if item.Title == "" {
			continue
		}

		title, company := parseWWRTitle(item.Title)

		var tags []string
		if item.Skills != "" {
			for _, s := range strings.Split(item.Skills, ",") {
				s = strings.TrimSpace(s)
				if s != "" {
					tags = append(tags, s)
				}
			}
		}

		posted := ""
		if item.PubDate != "" {
			if t, err := time.Parse(time.RFC1123Z, item.PubDate); err == nil {
				posted = t.UTC().Format("2006-01-02")
			} else if t, err := time.Parse(time.RFC1123, item.PubDate); err == nil {
				posted = t.UTC().Format("2006-01-02")
			}
		}

		location := item.Region
		if location == "" {
			location = "Anywhere"
		}

		jobType := item.Type
		if jobType == "" {
			jobType = "remote"
		}

		jobs = append(jobs, engine.RemoteJobListing{
			Title:    title,
			Company:  company,
			URL:      item.Link,
			Source:   "weworkremotely",
			Salary:   "not specified",
			Location: location,
			Tags:     tags,
			Posted:   posted,
			JobType:  jobType,
		})
	}

	return jobs, nil
}

// parseWWRTitle extracts company and title from "Company: Title" format.
func parseWWRTitle(raw string) (title, company string) {
	if idx := strings.Index(raw, ": "); idx > 0 {
		return strings.TrimSpace(raw[idx+2:]), strings.TrimSpace(raw[:idx])
	}
	return raw, ""
}

// filterRemoteJobs filters job listings by keyword match in title/company/tags.
// For single-keyword queries uses OR logic; for multi-keyword uses AND logic
// (all keywords must appear somewhere in the job's text).
func filterRemoteJobs(jobs []engine.RemoteJobListing, query string) []engine.RemoteJobListing {
	if query == "" {
		return jobs
	}

	keywords := strings.Fields(strings.ToLower(query))
	if len(keywords) == 0 {
		return jobs
	}

	var filtered []engine.RemoteJobListing
	for _, j := range jobs {
		haystack := strings.ToLower(j.Title + " " + j.Company + " " + strings.Join(j.Tags, " "))
		if matchesAllKeywords(haystack, keywords) {
			filtered = append(filtered, j)
		}
	}
	// If AND-match returns nothing, fall back to OR-match.
	if len(filtered) == 0 {
		for _, j := range jobs {
			haystack := strings.ToLower(j.Title + " " + j.Company + " " + strings.Join(j.Tags, " "))
			for _, kw := range keywords {
				if strings.Contains(haystack, kw) {
					filtered = append(filtered, j)
					break
				}
			}
		}
	}
	return filtered
}

// matchesAllKeywords returns true if haystack contains ALL keywords.
func matchesAllKeywords(haystack string, keywords []string) bool {
	for _, kw := range keywords {
		if !strings.Contains(haystack, kw) {
			return false
		}
	}
	return true
}

// stopWords are common words that make poor RemoteOK API tags.
var remoteOKStopWords = map[string]bool{
	"senior": true, "junior": true, "lead": true, "staff": true,
	"principal": true, "remote": true, "job": true, "jobs": true,
	"developer": true, "engineer": true, "position": true, "role": true,
	"and": true, "or": true, "the": true, "for": true, "with": true,
}

// pickBestRemoteOKTag picks the most specific keyword from the query for the RemoteOK tag filter.
// Skips generic stop words and prefers tech/language names.
func pickBestRemoteOKTag(fields []string) string {
	for _, f := range fields {
		if !remoteOKStopWords[f] && len(f) > 2 {
			return f
		}
	}
	return fields[0]
}

// RemoteJobsToSearxngResults converts remote job listings to engine.SearxngResult for LLM pipeline.
func RemoteJobsToSearxngResults(jobs []engine.RemoteJobListing) []engine.SearxngResult {
	results := make([]engine.SearxngResult, 0, len(jobs))
	for _, j := range jobs {
		var content strings.Builder
		content.WriteString("**Source:** " + j.Source)
		if j.Salary != "not specified" {
			content.WriteString(" | **Salary:** " + j.Salary)
		}
		if len(j.Tags) > 0 {
			content.WriteString(" | **Tags:** " + strings.Join(j.Tags, ", "))
		}
		if j.Location != "" {
			content.WriteString(" | **Location:** " + j.Location)
		}
		if j.JobType != "" {
			content.WriteString(" | **Type:** " + j.JobType)
		}
		if j.Posted != "" {
			content.WriteString(" | **Posted:** " + j.Posted)
		}

		titleParts := j.Title
		if j.Company != "" {
			titleParts = j.Company + ": " + j.Title
		}

		results = append(results, engine.SearxngResult{
			Title:   titleParts,
			Content: content.String(),
			URL:     j.URL,
			Score:   1.0,
		})
	}
	return results
}

// --- Remotive API ---

const remotiveAPI = "https://remotive.com/api/remote-jobs"

type remotiveResponse struct {
	JobCount int           `json:"job-count"`
	Jobs     []remotiveJob `json:"jobs"`
}

type remotiveJob struct {
	ID                        int      `json:"id"`
	URL                       string   `json:"url"`
	Title                     string   `json:"title"`
	CompanyName               string   `json:"company_name"`
	Tags                      []string `json:"tags"`
	JobType                   string   `json:"job_type"`
	PublicationDate           string   `json:"publication_date"`
	CandidateRequiredLocation string   `json:"candidate_required_location"`
	Salary                    string   `json:"salary"`
}

// SearchRemotive queries the Remotive public JSON API for remote job listings.
// No auth required. Search results are filtered by the `search` param server-side.
func SearchRemotive(ctx context.Context, query string, limit int) ([]engine.RemoteJobListing, error) {
	if limit <= 0 || limit > 30 {
		limit = 15
	}

	u, err := url.Parse(remotiveAPI)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("search", query)
	u.RawQuery = q.Encode()

	ctx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
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

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("remotive API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, err
	}

	var rr remotiveResponse
	if err := json.Unmarshal(body, &rr); err != nil {
		return nil, fmt.Errorf("remotive parse error: %w", err)
	}

	var jobs []engine.RemoteJobListing
	for _, j := range rr.Jobs {
		if j.Title == "" || j.URL == "" {
			continue
		}

		// Parse date: Remotive uses "2024-01-15T10:00:00" format, take YYYY-MM-DD prefix.
		posted := ""
		if len(j.PublicationDate) >= 10 {
			posted = j.PublicationDate[:10]
		}

		location := j.CandidateRequiredLocation
		if location == "" {
			location = "Worldwide"
		}

		jobType := strings.ReplaceAll(j.JobType, "_", " ")

		salary := j.Salary
		if salary == "" {
			salary = "not specified"
		}

		jobs = append(jobs, engine.RemoteJobListing{
			Title:    j.Title,
			Company:  j.CompanyName,
			URL:      j.URL,
			Source:   "remotive",
			Salary:   salary,
			Location: location,
			Tags:     j.Tags,
			Posted:   posted,
			JobType:  jobType,
		})
	}

	if len(jobs) > limit {
		jobs = jobs[:limit]
	}

	slog.Debug("remotive: search complete", slog.Int("results", len(jobs)))
	return jobs, nil
}

// llmRemoteWorkOutput is the JSON structure expected from the LLM for remote work search.
type llmRemoteWorkOutput struct {
	Jobs    []engine.RemoteJobListing `json:"jobs"`
	Summary string             `json:"summary"`
}

// SummarizeRemoteWorkResults calls the LLM with remote-work-specific prompt and parses structured jobs.
func SummarizeRemoteWorkResults(ctx context.Context, query, instruction string, contentLimit int, results []engine.SearxngResult, contents map[string]string) (*engine.RemoteWorkSearchOutput, error) {
	sources := engine.BuildSourcesText(results, contents, contentLimit)
	prompt := fmt.Sprintf("%s\n\nQuery: %s\n\nSources:\n%s", instruction, query, sources)

	raw, err := engine.CallLLM(ctx, prompt)
	if err != nil {
		return nil, err
	}

	var out llmRemoteWorkOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return &engine.RemoteWorkSearchOutput{Query: query, Summary: raw}, nil
	}

	// Fill URLs from search results for jobs that don't have them (immutable).
	enrichedJobs := make([]engine.RemoteJobListing, len(out.Jobs))
	for i, job := range out.Jobs {
		if job.URL == "" && i < len(results) {
			job.URL = results[i].URL
		}
		enrichedJobs[i] = job
	}

	return &engine.RemoteWorkSearchOutput{
		Query:   query,
		Jobs:    enrichedJobs,
		Summary: out.Summary,
	}, nil
}
