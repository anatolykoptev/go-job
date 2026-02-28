package jobserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/anatolykoptev/go_job/internal/engine/sources"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	platAll        = "all"
	platLinkedIn   = "linkedin"
	platGreenhouse = "greenhouse"
	platLever      = "lever"
	platIndeed     = "indeed"
	platATS        = "ats"
	platStartup    = "startup"
	platGoogle     = "google"
	platCraigslist = "craigslist"
	platRemoteOK    = "remoteok"
	platWWR         = "weworkremotely"
	platFreelancer  = "freelancer"
	platRemotive    = "remotive"
)

//nolint:funlen // multi-platform aggregation
func registerJobSearch(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "job_search",
		Description: "Search for job listings on LinkedIn, Greenhouse, Lever, YC workatastartup.com, HN Who is Hiring, Craigslist, RemoteOK, WeWorkRemotely, Remotive, and Freelancer. Returns structured JSON with job details (title, company, location, salary, skills, URL). Supports filters for experience level, job type, remote/onsite, time range, and platform.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input engine.JobSearchInput) (*mcp.CallToolResult, engine.JobSearchOutput, error) {
		if input.Query == "" {
			return nil, engine.JobSearchOutput{}, errors.New("query is required")
		}

		cacheKey := engine.CacheKey("job_search", input.Query, input.Location, input.Experience, input.JobType, input.Remote, input.TimeRange, input.Platform, fmt.Sprintf("limit_%d_offset_%d", input.Limit, input.Offset))
		if out, ok := engine.CacheLoadJSON[engine.JobSearchOutput](ctx, cacheKey); ok {
			return nil, out, nil
		}

		// Apply user profile defaults.
		profile := jobs.LoadProfile()
		if input.Platform == "" && profile.DefaultPlatform != "" {
			input.Platform = profile.DefaultPlatform
		}
		if input.Limit <= 0 && profile.DefaultLimit > 0 {
			input.Limit = profile.DefaultLimit
		}
		if input.Location == "" && profile.DefaultLocation != "" {
			input.Location = profile.DefaultLocation
		}
		if input.Remote == "" && profile.DefaultRemote != "" {
			input.Remote = profile.DefaultRemote
		}
		if input.Blacklist == "" && profile.Blacklist != "" {
			input.Blacklist = profile.Blacklist
		}

		lang := engine.NormLang(input.Language)

		platform := strings.ToLower(strings.TrimSpace(input.Platform))
		if platform == "" {
			platform = platAll
		}

		limit := input.Limit
		if limit <= 0 {
			limit = 15
		}
		if limit > 50 {
			limit = 50
		}

		useLinkedIn := platform == platAll || platform == platLinkedIn
		useGreenhouse := platform == platAll || platform == platGreenhouse || platform == platATS || platform == platStartup
		useLever := platform == platAll || platform == platLever || platform == platATS || platform == platStartup
		useYC := platform == platAll || platform == "yc" || platform == platStartup
		useHN := platform == platAll || platform == "hn" || platform == platStartup
		useIndeed := platform == platAll || platform == platIndeed
		useHabr := platform == platAll || platform == "habr"
		useTwitter := platform == platAll || platform == "twitter"
		useCraigslist := platform == platAll || platform == platCraigslist
		useRemoteOK := platform == platAll || platform == platRemoteOK || platform == "remote"
		useWWR := platform == platAll || platform == platWWR || platform == "remote"
		useRemotive := platform == platAll || platform == platRemotive || platform == "remote"
		useFreelancer := platform == platAll || platform == platFreelancer
		useGoogle := platform == platAll || platform == platGoogle

		type sourceResult struct {
			name    string
			results []engine.SearxngResult
			liJobs  []jobs.LinkedInJob
			err     error
		}

		var srcs []string
		if useLinkedIn {
			srcs = append(srcs, platLinkedIn)
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
		if useTwitter {
			srcs = append(srcs, "twitter")
		}
		if useCraigslist {
			srcs = append(srcs, platCraigslist)
		}
		if useRemoteOK {
			srcs = append(srcs, platRemoteOK)
		}
		if useWWR {
			srcs = append(srcs, platWWR)
		}
		if useRemotive {
			srcs = append(srcs, platRemotive)
		}
		if useFreelancer {
			srcs = append(srcs, platFreelancer)
		}
		if useGoogle {
			srcs = append(srcs, platGoogle)
		}

		ch := make(chan sourceResult, len(srcs)+1)

		for _, src := range srcs {
			go func(name string) {
				switch name {
				case platLinkedIn:
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

				case "twitter":
					results, err := jobs.SearchTwitterJobs(ctx, input.Query, 30)
					if err != nil {
						slog.Warn("job_search: twitter error", slog.Any("error", err))
					}
					ch <- sourceResult{name: name, results: results, err: err}

				case platCraigslist:
					results, err := jobs.SearchCraigslistJobs(ctx, input.Query, input.Location, 15)
					if err != nil {
						slog.Warn("job_search: craigslist error", slog.Any("error", err))
					}
					ch <- sourceResult{name: name, results: results, err: err}

				case platRemoteOK:
					rjobs, err := jobs.SearchRemoteOK(ctx, input.Query, 15)
					if err != nil {
						slog.Warn("job_search: remoteok error", slog.Any("error", err))
					}
					ch <- sourceResult{name: name, results: jobs.RemoteJobsToSearxngResults(rjobs), err: err}

				case platWWR:
					rjobs, err := jobs.SearchWeWorkRemotely(ctx, input.Query, 15)
					if err != nil {
						slog.Warn("job_search: weworkremotely error", slog.Any("error", err))
					}
					ch <- sourceResult{name: name, results: jobs.RemoteJobsToSearxngResults(rjobs), err: err}

				case platRemotive:
					rjobs, err := jobs.SearchRemotive(ctx, input.Query, 15)
					if err != nil {
						slog.Warn("job_search: remotive error", slog.Any("error", err))
					}
					ch <- sourceResult{name: name, results: jobs.RemoteJobsToSearxngResults(rjobs), err: err}

				case platFreelancer:
					projects, err := sources.SearchFreelancerAPI(ctx, input.Query, 10)
					if err != nil {
						slog.Warn("job_search: freelancer error", slog.Any("error", err))
					}
					ch <- sourceResult{name: name, results: sources.FreelancerProjectsToSearxngResults(projects), err: err}

				case platGoogle:
					searxQuery := input.Query + " " + input.Location + " site:careers.google.com OR site:jobs.google.com"
					results, err := engine.SearchSearXNG(ctx, searxQuery, lang, input.TimeRange, engine.DefaultSearchEngine)
					if err != nil {
						slog.Warn("job_search: google error", slog.Any("error", err))
					}
					ch <- sourceResult{name: name, results: results, err: err}
				}
			}(src)
		}

		go func() {
			searxQuery := buildJobSearxQuery(input.Query, input.Location, platform)
			results, err := engine.SearchSearXNG(ctx, searxQuery, lang, input.TimeRange, engine.DefaultSearchEngine)
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
			if r.name == platLinkedIn && len(r.liJobs) > 0 {
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

		// Apply blacklist filter.
		deduped = applyBlacklist(deduped, input.Blacklist)

		// Apply pagination offset.
		if input.Offset > 0 && input.Offset < len(deduped) {
			deduped = deduped[input.Offset:]
		} else if input.Offset >= len(deduped) {
			return nil, engine.JobSearchOutput{Query: input.Query, Summary: "No more results (offset beyond total)."}, nil
		}

		top := engine.DedupByDomain(deduped, limit)
		if len(top) > limit {
			top = top[:limit]
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

		engine.CacheStoreJSON(ctx, cacheKey, input.Query, *jobOut)
		return nil, *jobOut, nil
	})
}

func buildJobSearxQuery(query, location, platform string) string {
	var sitePart string
	switch platform {
	case platLinkedIn:
		sitePart = "site:linkedin.com/jobs"
	case platGreenhouse:
		sitePart = "site:boards.greenhouse.io"
	case "lever":
		sitePart = "site:jobs.lever.co"
	case "yc":
		sitePart = "site:workatastartup.com"
	case "hn":
		sitePart = "site:news.ycombinator.com \"who is hiring\""
	case platCraigslist:
		sitePart = "site:craigslist.org"
	case platRemoteOK:
		sitePart = "site:remoteok.com"
	case platWWR:
		sitePart = "site:weworkremotely.com"
	case platRemotive:
		sitePart = "site:remotive.com"
	case "remote":
		sitePart = "site:remoteok.com OR site:weworkremotely.com OR site:remotive.com"
	case platFreelancer:
		sitePart = "site:freelancer.com/projects"
	case platGoogle:
		sitePart = "site:careers.google.com OR site:jobs.google.com"
	default:
		sitePart = "jobs"
	}
	if location != "" {
		return query + " " + location + " " + sitePart
	}
	return query + " " + sitePart
}

func applyBlacklist(results []engine.SearxngResult, blacklist string) []engine.SearxngResult {
	if blacklist == "" {
		return results
	}
	var terms []string
	for _, t := range strings.Split(blacklist, ",") {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" {
			terms = append(terms, t)
		}
	}
	if len(terms) == 0 {
		return results
	}
	var filtered []engine.SearxngResult
	for _, r := range results {
		lower := strings.ToLower(r.Title + " " + r.Content)
		blocked := false
		for _, term := range terms {
			if strings.Contains(lower, term) {
				blocked = true
				break
			}
		}
		if !blocked {
			filtered = append(filtered, r)
		}
	}
	return filtered
}
