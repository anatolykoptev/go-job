package jobserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/anatolykoptev/go_job/internal/engine/sources"
	"github.com/anatolykoptev/go_job/internal/toolutil"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterTools registers all work-related search tools on the given MCP server:
// job_search, remote_work_search, freelance_search.
func RegisterTools(server *mcp.Server) {
	registerJobSearch(server)
	registerRemoteWorkSearch(server)
	registerFreelanceSearch(server)
}

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

		ch := make(chan sourceResult, len(srcs)+1)

		for _, src := range srcs {
			go func(name string) {
				switch name {
				case "linkedin":
					liJobs, err := jobs.SearchLinkedInJobs(ctx, input.Query, input.Location, input.Experience, input.JobType, input.Remote, input.TimeRange, input.Salary)
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

		seen := make(map[string]bool)
		var deduped []engine.SearxngResult
		for _, r := range merged {
			if r.URL != "" && !seen[r.URL] {
				seen[r.URL] = true
				deduped = append(deduped, r)
			}
		}

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

// --- remote_work_search ---

func registerRemoteWorkSearch(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "remote_work_search",
		Description: "Search for remote jobs on RemoteOK, WeWorkRemotely, and the web via SearXNG. Returns structured JSON with job details (title, company, salary, tags, source). Best for remote-first positions worldwide.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input engine.RemoteWorkSearchInput) (*mcp.CallToolResult, engine.SmartSearchOutput, error) {
		if input.Query == "" {
			return nil, engine.SmartSearchOutput{}, fmt.Errorf("query is required")
		}

		cacheKey := engine.CacheKey("remote_work_search", input.Query, input.Language)
		if cached, ok := engine.CacheGet(ctx, cacheKey); ok {
			return nil, cached, nil
		}

		lang := toolutil.NormLang(input.Language)

		type apiResult struct {
			jobList []engine.RemoteJobListing
			err     error
		}
		rokCh := make(chan apiResult, 1)
		wwrCh := make(chan apiResult, 1)

		go func() {
			j, err := jobs.SearchRemoteOK(ctx, input.Query, 20)
			rokCh <- apiResult{j, err}
		}()
		go func() {
			j, err := jobs.SearchWeWorkRemotely(ctx, input.Query, 20)
			wwrCh <- apiResult{j, err}
		}()

		type searchResult struct {
			results []engine.SearxngResult
			err     error
		}
		var searxChannels []chan searchResult

		addQuery := func(q, eng string) {
			ch := make(chan searchResult, 1)
			searxChannels = append(searxChannels, ch)
			go func() {
				r, err := engine.SearchSearXNG(ctx, q, lang, "", eng)
				ch <- searchResult{r, err}
			}()
		}
		addQuery(input.Query+" remote job", "google")
		addQuery(input.Query+" remote job", "bing")

		var rokRes, wwrRes apiResult
		for i := 0; i < 2; i++ {
			select {
			case r := <-rokCh:
				rokRes = r
			case r := <-wwrCh:
				wwrRes = r
			case <-ctx.Done():
				return nil, engine.SmartSearchOutput{}, ctx.Err()
			}
		}

		if rokRes.err != nil {
			slog.Warn("remote_work_search: RemoteOK error", slog.Any("error", rokRes.err))
		}
		if wwrRes.err != nil {
			slog.Warn("remote_work_search: WWR error", slog.Any("error", wwrRes.err))
		}

		var apiSearxResults []engine.SearxngResult
		apiURLs := make(map[string]bool)

		if len(rokRes.jobList) > 0 {
			converted := jobs.RemoteJobsToSearxngResults(rokRes.jobList)
			for _, r := range converted {
				apiURLs[r.URL] = true
			}
			apiSearxResults = append(apiSearxResults, converted...)
		}
		if len(wwrRes.jobList) > 0 {
			converted := jobs.RemoteJobsToSearxngResults(wwrRes.jobList)
			for _, r := range converted {
				apiURLs[r.URL] = true
			}
			apiSearxResults = append(apiSearxResults, converted...)
		}

		var webResults []engine.SearxngResult
		for _, ch := range searxChannels {
			select {
			case res := <-ch:
				if res.err != nil {
					slog.Warn("remote_work_search: SearXNG error", slog.Any("error", res.err))
					continue
				}
				webResults = append(webResults, res.results...)
			case <-ctx.Done():
				return nil, engine.SmartSearchOutput{}, ctx.Err()
			}
		}

		var merged []engine.SearxngResult
		merged = append(merged, apiSearxResults...)
		merged = append(merged, webResults...)

		if len(merged) == 0 {
			if rokRes.err != nil && wwrRes.err != nil {
				return nil, engine.SmartSearchOutput{}, fmt.Errorf("all sources failed")
			}
			out := engine.RemoteWorkSearchOutput{Query: input.Query, Summary: "No remote jobs found."}
			return remoteWorkResult(ctx, cacheKey, out)
		}

		seen := make(map[string]bool)
		var deduped []engine.SearxngResult
		for _, r := range merged {
			if !seen[r.URL] {
				seen[r.URL] = true
				deduped = append(deduped, r)
			}
		}
		deduped = engine.DedupByDomain(deduped, 5)
		if len(deduped) > 15 {
			deduped = deduped[:15]
		}

		contents := toolutil.FetchURLsParallel(ctx, deduped, apiURLs)

		remoteOut, err := jobs.SummarizeRemoteWorkResults(ctx, input.Query, engine.RemoteWorkInstruction, 4000, deduped, contents)
		if err != nil {
			return nil, engine.SmartSearchOutput{}, fmt.Errorf("LLM summarization failed: %w", err)
		}

		enrichedJobs := make([]engine.RemoteJobListing, len(remoteOut.Jobs))
		for i, job := range remoteOut.Jobs {
			if job.URL == "" && i < len(deduped) {
				job.URL = deduped[i].URL
			}
			enrichedJobs[i] = job
		}

		return remoteWorkResult(ctx, cacheKey, engine.RemoteWorkSearchOutput{
			Query:   remoteOut.Query,
			Jobs:    enrichedJobs,
			Summary: remoteOut.Summary,
		})
	})
}

func remoteWorkResult(ctx context.Context, cacheKey string, out engine.RemoteWorkSearchOutput) (*mcp.CallToolResult, engine.SmartSearchOutput, error) {
	jsonBytes, err := json.Marshal(out)
	if err != nil {
		return nil, engine.SmartSearchOutput{}, fmt.Errorf("json marshal: %w", err)
	}
	result := engine.SmartSearchOutput{
		Query:   out.Query,
		Answer:  string(jsonBytes),
		Sources: []engine.SourceItem{},
	}
	engine.CacheSet(ctx, cacheKey, result)
	return nil, result, nil
}

// --- freelance_search ---

func registerFreelanceSearch(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "freelance_search",
		Description: "Search for freelance projects and gigs on Upwork and Freelancer.com. Returns structured JSON with project details (title, budget, skills, platform, URL). Freelancer.com uses direct API for rich data (budgets, bids, skills). Filter by platform.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input engine.FreelanceSearchInput) (*mcp.CallToolResult, engine.FreelanceSearchOutput, error) {
		if input.Query == "" {
			return nil, engine.FreelanceSearchOutput{}, fmt.Errorf("query is required")
		}

		cacheKey := engine.CacheKey("freelance_search", input.Query, input.Platform, input.Language)
		if out, ok := toolutil.CacheLoadJSON[engine.FreelanceSearchOutput](ctx, cacheKey); ok {
			return nil, out, nil
		}

		platform := strings.ToLower(input.Platform)
		lang := toolutil.NormLang(input.Language)

		useUpwork := platform == "" || platform == "all" || platform == "upwork"
		useFreelancer := platform == "" || platform == "all" || platform == "freelancer"

		var freelancerAPIResults []engine.SearxngResult
		freelancerAPISuccess := false
		if useFreelancer {
			projects, err := sources.SearchFreelancerAPI(ctx, input.Query, 10)
			if err != nil {
				slog.Warn("freelance_search: freelancer API error", slog.Any("error", err))
			} else if len(projects) > 0 {
				freelancerAPISuccess = true
				freelancerAPIResults = sources.FreelancerProjectsToSearxngResults(projects)
			}
		}

		type searchResult struct {
			results []engine.SearxngResult
			err     error
		}
		var channels []chan searchResult

		addQuery := func(q, eng string) {
			ch := make(chan searchResult, 1)
			channels = append(channels, ch)
			go func() {
				r, err := engine.SearchSearXNG(ctx, q, lang, "", eng)
				ch <- searchResult{r, err}
			}()
		}

		if useUpwork {
			addQuery(input.Query+" site:upwork.com/freelance-jobs/apply", "google")
			addQuery(input.Query+" site:upwork.com/freelance-jobs/apply", "bing")
		}
		if useFreelancer && !freelancerAPISuccess {
			addQuery(input.Query+" site:freelancer.com/projects", "google")
			addQuery(input.Query+" site:freelancer.com/projects", "bing")
		}

		var merged []engine.SearxngResult
		var lastErr error
		for _, ch := range channels {
			res := <-ch
			if res.err != nil {
				lastErr = res.err
				slog.Warn("freelance_search: search error", slog.Any("error", res.err))
				continue
			}
			merged = append(merged, res.results...)
		}

		apiURLs := make(map[string]bool, len(freelancerAPIResults))
		for _, r := range freelancerAPIResults {
			apiURLs[r.URL] = true
		}
		merged = append(freelancerAPIResults, merged...)

		if len(merged) == 0 {
			if lastErr != nil {
				return nil, engine.FreelanceSearchOutput{}, fmt.Errorf("search failed: %w", lastErr)
			}
			return nil, engine.FreelanceSearchOutput{Query: input.Query, Summary: "No results found."}, nil
		}

		seen := make(map[string]bool)
		var deduped []engine.SearxngResult
		for _, r := range merged {
			if !seen[r.URL] {
				seen[r.URL] = true
				deduped = append(deduped, r)
			}
		}

		var filtered []engine.SearxngResult
		for _, r := range deduped {
			u, err := url.Parse(r.URL)
			if err != nil {
				filtered = append(filtered, r)
				continue
			}
			host := u.Hostname()
			if strings.Contains(host, "upwork") {
				p := u.Path
				if !strings.Contains(p, "/apply/") && !strings.Contains(p, "/~") {
					continue
				}
			}
			filtered = append(filtered, r)
		}

		maxPerDomain := 10
		if useUpwork && useFreelancer {
			maxPerDomain = 5
		}
		top := engine.DedupByDomain(filtered, maxPerDomain)
		if len(top) > 10 {
			top = top[:10]
		}

		contents := toolutil.FetchURLsParallel(ctx, top, apiURLs)

		freelanceOut, err := engine.SummarizeFreelanceResults(ctx, input.Query, engine.FreelanceSearchInstruction, 4000, top, contents)
		if err != nil {
			return nil, engine.FreelanceSearchOutput{}, fmt.Errorf("LLM summarization failed: %w", err)
		}

		for i := range freelanceOut.Projects {
			p := &freelanceOut.Projects[i]
			if p.URL == "" && i < len(top) {
				p.URL = top[i].URL
			}
			if p.Platform == "" && p.URL != "" {
				if u, err := url.Parse(p.URL); err == nil {
					host := u.Hostname()
					if strings.Contains(host, "upwork") {
						p.Platform = "upwork"
					} else if strings.Contains(host, "freelancer") {
						p.Platform = "freelancer"
					}
				}
			}
		}

		toolutil.CacheStoreJSON(ctx, cacheKey, input.Query, *freelanceOut)
		return nil, *freelanceOut, nil
	})
}
