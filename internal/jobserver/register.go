package jobserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/anatolykoptev/go_job/internal/engine/sources"
	"github.com/anatolykoptev/go_job/internal/toolutil"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterTools registers all work-related search tools on the given MCP server.
func RegisterTools(server *mcp.Server) {
	// Search
	registerJobSearch(server)
	registerRemoteWorkSearch(server)
	registerFreelanceSearch(server)
	registerJobMatchScore(server)
	// Research
	registerSalaryResearch(server)
	registerCompanyResearch(server)
	// Resume
	registerResumeAnalyze(server)
	registerCoverLetterGenerate(server)
	registerResumeTailor(server)
	// Tracker
	registerJobTrackerAdd(server)
	registerJobTrackerList(server)
	registerJobTrackerUpdate(server)
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
		remCh := make(chan apiResult, 1)

		go func() {
			j, err := jobs.SearchRemoteOK(ctx, input.Query, 20)
			rokCh <- apiResult{j, err}
		}()
		go func() {
			j, err := jobs.SearchWeWorkRemotely(ctx, input.Query, 20)
			wwrCh <- apiResult{j, err}
		}()
		go func() {
			j, err := jobs.SearchRemotive(ctx, input.Query, 15)
			remCh <- apiResult{j, err}
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

		var rokRes, wwrRes, remRes apiResult
		for i := 0; i < 3; i++ {
			select {
			case r := <-rokCh:
				rokRes = r
			case r := <-wwrCh:
				wwrRes = r
			case r := <-remCh:
				remRes = r
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
		if remRes.err != nil {
			slog.Warn("remote_work_search: Remotive error", slog.Any("error", remRes.err))
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
		if len(remRes.jobList) > 0 {
			converted := jobs.RemoteJobsToSearxngResults(remRes.jobList)
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
			if rokRes.err != nil && wwrRes.err != nil && remRes.err != nil {
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

// --- job_match_score ---

func registerJobMatchScore(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "job_match_score",
		Description: "Score job listings against a resume using keyword overlap analysis (Jaccard similarity). Searches jobs across LinkedIn, Indeed, and YC, then ranks each result by how well it matches the resume text. Returns jobs sorted by match_score (0–100) with lists of matching and missing keywords.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input engine.JobMatchScoreInput) (*mcp.CallToolResult, engine.JobMatchScoreOutput, error) {
		if input.Resume == "" {
			return nil, engine.JobMatchScoreOutput{}, fmt.Errorf("resume is required")
		}
		if input.Query == "" {
			return nil, engine.JobMatchScoreOutput{}, fmt.Errorf("query is required")
		}

		resumeKW := jobs.ExtractResumeKeywords(input.Resume)

		platform := strings.ToLower(strings.TrimSpace(input.Platform))
		if platform == "" {
			platform = "all"
		}

		type srcResult struct {
			results []engine.SearxngResult
			source  string
		}

		var mu sync.Mutex
		var allResults []engine.SearxngResult
		var wg sync.WaitGroup

		if platform == "all" || platform == "linkedin" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				liJobs, err := jobs.SearchLinkedInJobs(ctx, input.Query, input.Location, "", "", "", "", "", 50, false)
				if err != nil {
					slog.Warn("job_match_score: linkedin error", slog.Any("error", err))
					return
				}
				rs := jobs.LinkedInJobsToSearxngResults(ctx, liJobs, 5)
				mu.Lock()
				allResults = append(allResults, rs...)
				mu.Unlock()
			}()
		}

		if platform == "all" || platform == "indeed" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rs, err := jobs.SearchIndeedJobsFiltered(ctx, input.Query, input.Location, "", "", 15)
				if err != nil {
					slog.Warn("job_match_score: indeed error", slog.Any("error", err))
					return
				}
				mu.Lock()
				allResults = append(allResults, rs...)
				mu.Unlock()
			}()
		}

		if platform == "all" || platform == "yc" || platform == "startup" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rs, err := jobs.SearchYCJobs(ctx, input.Query, input.Location, 10)
				if err != nil {
					slog.Warn("job_match_score: yc error", slog.Any("error", err))
					return
				}
				mu.Lock()
				allResults = append(allResults, rs...)
				mu.Unlock()
			}()
		}

		if platform == "all" || platform == "hn" || platform == "startup" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rs, err := jobs.SearchHNJobs(ctx, input.Query, 10)
				if err != nil {
					slog.Warn("job_match_score: hn error", slog.Any("error", err))
					return
				}
				mu.Lock()
				allResults = append(allResults, rs...)
				mu.Unlock()
			}()
		}

		wg.Wait()

		if len(allResults) == 0 {
			return nil, engine.JobMatchScoreOutput{Query: input.Query, Summary: "No jobs found."}, nil
		}

		// Dedup by URL.
		seen := make(map[string]bool)
		var deduped []engine.SearxngResult
		for _, r := range allResults {
			if r.URL != "" && !seen[r.URL] {
				seen[r.URL] = true
				deduped = append(deduped, r)
			}
		}

		// Score each result against resume keywords.
		scored := make([]engine.JobMatchResult, 0, len(deduped))
		for _, r := range deduped {
			jobText := r.Title + " " + r.Content
			score, matching, missing := jobs.ScoreJobMatch(resumeKW, jobText)

			// Split "Title at Company" LinkedIn format into separate fields.
			title, company := r.Title, ""
			if parts := strings.SplitN(r.Title, " at ", 2); len(parts) == 2 {
				title = parts[0]
				company = parts[1]
			}

			snippet := engine.TruncateRunes(r.Content, 300, "...")

			scored = append(scored, engine.JobMatchResult{
				Title:            title,
				Company:          company,
				URL:              r.URL,
				Source:           extractSource(r.URL),
				Snippet:          snippet,
				MatchScore:       score,
				MatchingKeywords: matching,
				MissingKeywords:  missing,
			})
		}

		// Sort by score descending.
		sort.Slice(scored, func(i, j int) bool {
			return scored[i].MatchScore > scored[j].MatchScore
		})
		if len(scored) > 15 {
			scored = scored[:15]
		}

		topScore := 0.0
		if len(scored) > 0 {
			topScore = scored[0].MatchScore
		}
		summary := fmt.Sprintf("Scored %d jobs for %q. Top match: %.1f/100.", len(scored), input.Query, topScore)

		return nil, engine.JobMatchScoreOutput{
			Query:   input.Query,
			Jobs:    scored,
			Summary: summary,
		}, nil
	})
}

func registerSalaryResearch(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "salary_research",
		Description: "Research salary ranges for a role and location. Returns p25/median/p75 percentiles with sources (levels.fyi, Glassdoor, LinkedIn, hh.ru, Хабр). For Russian locations returns RUB, otherwise USD.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.SalaryResearchInput) (*mcp.CallToolResult, *jobs.SalaryResearchResult, error) {
		if input.Role == "" {
			return nil, nil, fmt.Errorf("role is required")
		}
		result, err := jobs.ResearchSalary(ctx, input.Role, input.Location, input.Experience)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}

func registerCompanyResearch(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "company_research",
		Description: "Research a company for interview preparation or job evaluation. Returns size, funding, tech stack, culture notes, recent news, Glassdoor rating, and an overall summary for job seekers.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.CompanyResearchInput) (*mcp.CallToolResult, *jobs.CompanyResearchResult, error) {
		if input.Company == "" {
			return nil, nil, fmt.Errorf("company is required")
		}
		result, err := jobs.ResearchCompany(ctx, input.Company)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}

func registerResumeAnalyze(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "resume_analyze",
		Description: "Analyze a resume against a job description. Returns ATS score (0-100), matching/missing keywords, experience gaps, and specific recommendations to improve match rate.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.ResumeAnalyzeInput) (*mcp.CallToolResult, *jobs.ResumeAnalysisResult, error) {
		if input.Resume == "" {
			return nil, nil, fmt.Errorf("resume is required")
		}
		if input.JobDescription == "" {
			return nil, nil, fmt.Errorf("job_description is required")
		}
		result, err := jobs.AnalyzeResume(ctx, input.Resume, input.JobDescription)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}

func registerCoverLetterGenerate(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "cover_letter_generate",
		Description: "Generate a tailored cover letter from a resume and job description. Tone options: professional (default), friendly, concise. Returns the cover letter text with word count.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.CoverLetterInput) (*mcp.CallToolResult, *jobs.CoverLetterResult, error) {
		if input.Resume == "" {
			return nil, nil, fmt.Errorf("resume is required")
		}
		if input.JobDescription == "" {
			return nil, nil, fmt.Errorf("job_description is required")
		}
		result, err := jobs.GenerateCoverLetter(ctx, input.Resume, input.JobDescription, input.Tone)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}

func registerResumeTailor(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "resume_tailor",
		Description: "Rewrite resume sections to better match a specific job description. Incorporates missing keywords naturally, reorders bullet points by relevance, quantifies achievements. Returns tailored resume + diff summary.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.ResumeTailorInput) (*mcp.CallToolResult, *jobs.ResumeTailorResult, error) {
		if input.Resume == "" {
			return nil, nil, fmt.Errorf("resume is required")
		}
		if input.JobDescription == "" {
			return nil, nil, fmt.Errorf("job_description is required")
		}
		result, err := jobs.TailorResume(ctx, input.Resume, input.JobDescription)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}

func registerJobTrackerAdd(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "job_tracker_add",
		Description: "Save a job to the local tracker (SQLite). Status options: saved (default), applied, interview, offer, rejected. Returns the assigned ID for future updates.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input jobs.JobTrackerAddInput) (*mcp.CallToolResult, *jobs.JobTrackerResult, error) {
		if input.Title == "" || input.Company == "" {
			return nil, nil, fmt.Errorf("title and company are required")
		}
		result, err := jobs.AddTrackedJob(ctx, input)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}

func registerJobTrackerList(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "job_tracker_list",
		Description: "List tracked job applications. Optionally filter by status: saved, applied, interview, offer, rejected. Returns jobs sorted by most recently updated.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input jobs.JobTrackerListInput) (*mcp.CallToolResult, *jobs.JobTrackerListResult, error) {
		result, err := jobs.ListTrackedJobs(ctx, input)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}

func registerJobTrackerUpdate(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "job_tracker_update",
		Description: "Update status or notes for a tracked job by ID. Status options: saved, applied, interview, offer, rejected. Get IDs from job_tracker_list.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input jobs.JobTrackerUpdateInput) (*mcp.CallToolResult, *jobs.JobTrackerResult, error) {
		if input.ID <= 0 {
			return nil, nil, fmt.Errorf("id is required")
		}
		result, err := jobs.UpdateTrackedJob(ctx, input)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}

// extractSource guesses the job board name from a URL hostname.
func extractSource(jobURL string) string {
	u, err := url.Parse(jobURL)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	switch {
	case strings.Contains(host, "linkedin"):
		return "linkedin"
	case strings.Contains(host, "indeed"):
		return "indeed"
	case strings.Contains(host, "workatastartup"):
		return "yc"
	case strings.Contains(host, "ycombinator"):
		return "hn"
	case strings.Contains(host, "greenhouse"):
		return "greenhouse"
	case strings.Contains(host, "lever"):
		return "lever"
	case strings.Contains(host, "remoteok"):
		return "remoteok"
	case strings.Contains(host, "remotive"):
		return "remotive"
	default:
		return host
	}
}
