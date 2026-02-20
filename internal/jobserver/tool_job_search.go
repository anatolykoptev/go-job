package jobserver

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/anatolykoptev/go_job/internal/toolutil"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerJobSearch(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "job_search",
		Description: "Search for job listings on LinkedIn, Greenhouse, Lever, YC workatastartup.com, and HN Who is Hiring. Returns structured JSON with job details (title, company, location, salary, skills, URL). Supports filters for experience level, job type, remote/onsite, time range, and platform.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input engine.JobSearchInput) (*mcp.CallToolResult, engine.JobSearchOutput, error) {
		if input.Query == "" {
			return nil, engine.JobSearchOutput{}, fmt.Errorf("query is required")
		}

		cacheKey := engine.CacheKey("job_search", input.Query, input.Location, input.Experience, input.JobType, input.Remote, input.TimeRange, input.Platform)
		if out, ok := toolutil.CacheLoadJSON[engine.JobSearchOutput](ctx, cacheKey); ok {
			return nil, out, nil
		}

		lang := toolutil.NormLang(input.Language)

		platform := strings.ToLower(strings.TrimSpace(input.Platform))
		if platform == "" {
			platform = "all"
		}

		useLinkedIn := platform == "all" || platform == "linkedin"
		useGreenhouse := platform == "all" || platform == "greenhouse" || platform == "ats" || platform == "startup"
		useLever := platform == "all" || platform == "lever" || platform == "ats" || platform == "startup"
		useYC := platform == "all" || platform == "yc" || platform == "startup"
		useHN := platform == "all" || platform == "hn" || platform == "startup"
		useIndeed := platform == "all" || platform == "indeed"
		useHabr := platform == "all" || platform == "habr"

		type sourceResult struct {
			name    string
			results []engine.SearxngResult
			liJobs  []jobs.LinkedInJob
			err     error
		}

		var srcs []string
		if useLinkedIn {
			srcs = append(srcs, "linkedin")
		}
		if useGreenhouse {
			srcs = append(srcs, "greenhouse")
		}
		if useLever {
			srcs = append(srcs, "lever")
		}
		if useYC {
			srcs = append(srcs, "yc")
		}
		if useHN {
			srcs = append(srcs, "hn")
		}
		if useIndeed {
			srcs = append(srcs, "indeed")
		}
		if useHabr {
			srcs = append(srcs, "habr")
		}

		ch := make(chan sourceResult, len(srcs)+1)

		for _, src := range srcs {
			go func(name string) {
				switch name {
				case "linkedin":
					liJobs, err := jobs.SearchLinkedInJobs(ctx, input.Query, input.Location, input.Experience, input.JobType, input.Remote, input.TimeRange, input.Salary, 50, input.EasyApply)
					if err != nil {
						slog.Warn("job_search: linkedin error", slog.Any("error", err))
						ch <- sourceResult{name: name, err: err}
						return
					}
					slog.Info("job_search: linkedin returned jobs", slog.Int("count", len(liJobs)))
					ch <- sourceResult{name: name, results: jobs.LinkedInJobsToSearxngResults(ctx, liJobs, 8), liJobs: liJobs}

				case "greenhouse":
					results, err := jobs.SearchGreenhouseJobs(ctx, input.Query, input.Location, 10)
					if err != nil {
						slog.Warn("job_search: greenhouse error", slog.Any("error", err))
					}
					ch <- sourceResult{name: name, results: results, err: err}

				case "lever":
					results, err := jobs.SearchLeverJobs(ctx, input.Query, input.Location, 10)
					if err != nil {
						slog.Warn("job_search: lever error", slog.Any("error", err))
					}
					ch <- sourceResult{name: name, results: results, err: err}

				case "yc":
					results, err := jobs.SearchYCJobs(ctx, input.Query, input.Location, 10)
					if err != nil {
						slog.Warn("job_search: yc error", slog.Any("error", err))
					}
					ch <- sourceResult{name: name, results: results, err: err}

				case "hn":
					results, err := jobs.SearchHNJobs(ctx, input.Query, 20)
					if err != nil {
						slog.Warn("job_search: hn error", slog.Any("error", err))
					}
					ch <- sourceResult{name: name, results: results, err: err}

				case "indeed":
					results, err := jobs.SearchIndeedJobsFiltered(ctx, input.Query, input.Location, input.JobType, input.TimeRange, 15)
					if err != nil {
						slog.Warn("job_search: indeed error", slog.Any("error", err))
					}
					ch <- sourceResult{name: name, results: results, err: err}

				case "habr":
					results, err := jobs.SearchHabrJobs(ctx, input.Query, input.Location, 10)
					if err != nil {
						slog.Warn("job_search: habr error", slog.Any("error", err))
					}
					ch <- sourceResult{name: name, results: results, err: err}
				}
			}(src)
		}

		go func() {
			searxQuery := buildJobSearxQuery(input.Query, input.Location, platform)
			results, err := engine.SearchSearXNG(ctx, searxQuery, lang, input.TimeRange, "google")
			if err != nil {
				slog.Warn("job_search: searxng error", slog.Any("error", err))
			}
			ch <- sourceResult{name: "searxng", results: results, err: err}
		}()

		totalGoroutines := len(srcs) + 1
		var merged []engine.SearxngResult
		var linkedInJobs []jobs.LinkedInJob
		for i := 0; i < totalGoroutines; i++ {
			r := <-ch
			merged = append(merged, r.results...)
			if r.name == "linkedin" && len(r.liJobs) > 0 {
				linkedInJobs = r.liJobs
			}
		}

		if len(merged) == 0 {
			return nil, engine.JobSearchOutput{Query: input.Query, Summary: "No results found."}, nil
		}

		// Dedup pass 1: by URL.
		seen := make(map[string]bool)
		var deduped []engine.SearxngResult
		for _, r := range merged {
			if r.URL != "" && !seen[r.URL] {
				seen[r.URL] = true
				deduped = append(deduped, r)
			}
		}

		// Dedup pass 2: by canonical key (same job from different sources).
		canonSeen := make(map[string]bool)
		var canonDeduped []engine.SearxngResult
		for _, r := range deduped {
			key := engine.CanonicalJobKey(r.Title, "")
			if !canonSeen[key] {
				canonSeen[key] = true
				canonDeduped = append(canonDeduped, r)
			}
		}
		deduped = canonDeduped

		top := engine.DedupByDomain(deduped, 15)
		if len(top) > 15 {
			top = top[:15]
		}

		contents := make(map[string]string)
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, r := range top {
			if r.Content != "" && strings.Contains(r.Content, "**Source:**") {
				mu.Lock()
				contents[r.URL] = r.Content
				mu.Unlock()
				continue
			}
			if r.Content != "" && strings.Contains(r.Content, "**") {
				mu.Lock()
				contents[r.URL] = r.Content
				mu.Unlock()
				continue
			}
			wg.Add(1)
			go func(u string) {
				defer wg.Done()
				_, text, err := engine.FetchURLContent(ctx, u)
				if err == nil && text != "" {
					mu.Lock()
					contents[u] = text
					mu.Unlock()
				}
			}(r.URL)
		}
		wg.Wait()

		jobOut, err := engine.SummarizeJobResults(ctx, input.Query, engine.JobSearchInstruction, 5000, top, contents)
		if err != nil {
			return nil, engine.JobSearchOutput{}, fmt.Errorf("LLM summarization failed: %w", err)
		}

		liByJobID := make(map[string]*jobs.LinkedInJob)
		for i := range linkedInJobs {
			if linkedInJobs[i].JobID != "" {
				liByJobID[linkedInJobs[i].JobID] = &linkedInJobs[i]
			}
		}

		for i := range jobOut.Jobs {
			j := &jobOut.Jobs[i]
			if j.URL == "" && i < len(top) {
				j.URL = top[i].URL
			}
			if j.JobID == "" && j.URL != "" {
				j.JobID = jobs.ExtractJobID(j.URL)
			}
			if lj, ok := liByJobID[j.JobID]; ok {
				if j.Company == "" {
					j.Company = lj.Company
				}
				if j.Location == "" {
					j.Location = lj.Location
				}
				if j.Posted == "" || j.Posted == "not specified" {
					j.Posted = lj.Posted
				}
			}
		}

		toolutil.CacheStoreJSON(ctx, cacheKey, input.Query, *jobOut)
		return nil, *jobOut, nil
	})
}

func buildJobSearxQuery(query, location, platform string) string {
	var sitePart string
	switch platform {
	case "linkedin":
		sitePart = "site:linkedin.com/jobs"
	case "greenhouse":
		sitePart = "site:boards.greenhouse.io"
	case "lever":
		sitePart = "site:jobs.lever.co"
	case "yc":
		sitePart = "site:workatastartup.com"
	case "hn":
		sitePart = "site:news.ycombinator.com \"who is hiring\""
	default:
		sitePart = "jobs"
	}
	if location != "" {
		return query + " " + location + " " + sitePart
	}
	return query + " " + sitePart
}
