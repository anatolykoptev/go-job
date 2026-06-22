package jobserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/anatolykoptev/go_job/internal/engine/sources"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	platAll        = "all"
	platLinkedIn   = "linkedin"
	platGreenhouse = "greenhouse"
	platLever      = "lever"
	platAshby      = "ashby"
	platYC         = "yc"
	platHN         = "hn"
	platHabr       = "habr"
	platIndeed     = "indeed"
	platATS        = "ats"
	platStartup    = "startup"
	platGoogle     = "google"
	platCraigslist = "craigslist"
	platRemoteOK   = "remoteok"
	platWWR        = "weworkremotely"
	platFreelancer = "freelancer"
	platRemotive   = "remotive"
	platRemote     = "remote"
	platTwitter    = "twitter"
	platInspira    = "inspira" // UN Secretariat careers.un.org
	platUNDP       = "undp"    // UNDP Oracle HCM jobs portal
	platUN         = "un"      // meta-platform fan-out: inspira + undp
)

//nolint:funlen // multi-platform aggregation
func registerJobSearch(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "job_search",
		Description: "Search for job listings on LinkedIn, Greenhouse, Lever, Ashby, YC workatastartup.com, HN Who is Hiring, Craigslist, RemoteOK, WeWorkRemotely, Remotive, Freelancer, Inspira (careers.un.org UN Secretariat), and UNDP (jobs.undp.org). Returns structured JSON with job details (title, company, location, salary, skills, URL). Supports filters for experience level, job type, remote/onsite, time range, and platform. UN sources are opt-in: platform=inspira queries careers.un.org only, platform=undp queries jobs.undp.org only, platform=un fans out to both. The default platform=all DOES NOT query Inspira or UNDP — set platform explicitly when looking for UN-system openings. raw=true skips LLM processing and returns raw tweet objects — only meaningful when platform=twitter.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input engine.JobSearchInput) (*mcp.CallToolResult, engine.JobSearchOutput, error) {
		if input.Query == "" {
			return nil, engine.JobSearchOutput{}, errors.New("query is required")
		}

		// raw=true with platform=twitter: bypass fan-out and LLM, return raw tweet objects.
		if input.Raw && strings.ToLower(strings.TrimSpace(input.Platform)) == platTwitter {
			rawTweets, err := jobs.SearchTwitterJobsRaw(ctx, input.Query, 30)
			if err != nil {
				return nil, engine.JobSearchOutput{}, fmt.Errorf("twitter raw search: %w", err)
			}
			encoded, mErr := json.Marshal(rawTweets)
			if mErr != nil {
				return nil, engine.JobSearchOutput{}, fmt.Errorf("marshal raw tweets: %w", mErr)
			}
			cr := &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}},
			}
			return cr, engine.JobSearchOutput{}, nil
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

		type sourceResult struct {
			name    string
			results []engine.SearxngResult
			liJobs  []jobs.LinkedInJob
			err     error
		}

		srcs := selectSources(platform)

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

				case platGreenhouse:
					results, err := jobs.SearchGreenhouseJobs(ctx, input.Query, input.Location, 10)
					if err != nil {
						slog.Warn("job_search: greenhouse error", slog.Any("error", err))
					}
					ch <- sourceResult{name: name, results: results, err: err}

				case platLever:
					results, err := jobs.SearchLeverJobs(ctx, input.Query, input.Location, 10)
					if err != nil {
						slog.Warn("job_search: lever error", slog.Any("error", err))
					}
					ch <- sourceResult{name: name, results: results, err: err}

				case platAshby:
					results, err := jobs.SearchAshbyJobs(ctx, input.Query, input.Location, 10)
					if err != nil {
						slog.Warn("job_search: ashby error", slog.Any("error", err))
					}
					ch <- sourceResult{name: name, results: results, err: err}

				case platYC:
					results, err := jobs.SearchYCJobs(ctx, input.Query, input.Location, 10)
					if err != nil {
						slog.Warn("job_search: yc error", slog.Any("error", err))
					}
					ch <- sourceResult{name: name, results: results, err: err}

				case platHN:
					results, err := jobs.SearchHNJobs(ctx, input.Query, 20)
					if err != nil {
						slog.Warn("job_search: hn error", slog.Any("error", err))
					}
					ch <- sourceResult{name: name, results: results, err: err}

				case platIndeed:
					results, err := jobs.SearchIndeedJobsFiltered(ctx, input.Query, input.Location, input.JobType, input.TimeRange, 15)
					if err != nil {
						slog.Warn("job_search: indeed error", slog.Any("error", err))
					}
					ch <- sourceResult{name: name, results: results, err: err}

				case platHabr:
					results, err := jobs.SearchHabrJobs(ctx, input.Query, input.Location, 10)
					if err != nil {
						slog.Warn("job_search: habr error", slog.Any("error", err))
					}
					ch <- sourceResult{name: name, results: results, err: err}

				case platTwitter:
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
					// go-engine DIRECT (primary, always-on) + SearXNG (additive when configured).
					results := engine.SearchDirect(ctx, searxQuery, lang)
					searx, err := engine.SearchSearXNG(ctx, searxQuery, lang, input.TimeRange, engine.DefaultSearchEngine)
					if err != nil {
						slog.Warn("job_search: google searxng error (additive)", slog.Any("error", err))
					}
					results = append(results, searx...)
					engine.IncrPlatformResults(platGoogle, engine.PlatformOutcome(len(results), nil))
					ch <- sourceResult{name: name, results: results, err: nil}

				case platInspira:
					results, err := jobs.SearchInspiraJobs(ctx, input.Query, input.Location, limit)
					if err != nil {
						slog.Warn("job_search: inspira error", slog.Any("error", err))
					}
					ch <- sourceResult{name: name, results: results, err: err}

				case platUNDP:
					results, err := jobs.SearchUNDPJobs(ctx, input.Query, input.Location, limit)
					if err != nil {
						slog.Warn("job_search: undp error", slog.Any("error", err))
					}
					ch <- sourceResult{name: name, results: results, err: err}
				}
			}(src)
		}

		go func() {
			searxQuery := buildJobSearxQuery(input.Query, input.Location, platform)
			// go-engine DIRECT (primary, always-on via DIRECT_* env) + SearXNG (additive).
			results := engine.SearchDirect(ctx, searxQuery, lang)
			searx, err := engine.SearchSearXNG(ctx, searxQuery, lang, input.TimeRange, engine.DefaultSearchEngine)
			if err != nil {
				slog.Warn("job_search: searxng error (additive)", slog.Any("error", err))
			}
			results = append(results, searx...)
			engine.IncrPlatformResults("searxng", engine.PlatformOutcome(len(results), nil))
			ch <- sourceResult{name: "searxng", results: results, err: nil}
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

		persistJobListings(ctx, jobOut.Jobs)
		engine.CacheStoreJSON(ctx, cacheKey, input.Query, *jobOut)
		if cr, spilled := handleSpill(ctx, "job_search", *jobOut); spilled {
			var zero engine.JobSearchOutput
			return cr, zero, nil
		}
		return nil, *jobOut, nil
	})
}

// selectSources maps a normalized platform filter to the ordered list of
// connector source names that job_search fans out to. platform=all selects every
// commercial connector; meta-platforms (ats/startup/remote/un) expand to their
// members; UN scrapers (inspira/undp) stay opt-in and are NOT included by
// platform=all. The returned names are exactly the case labels in the per-source
// dispatch switch — the regression test asserts every advertised platform routes
// to a non-empty, correctly-named source set.
func selectSources(platform string) []string {
	use := map[string]bool{
		platLinkedIn:   platform == platAll || platform == platLinkedIn,
		platGreenhouse: platform == platAll || platform == platGreenhouse || platform == platATS || platform == platStartup,
		platLever:      platform == platAll || platform == platLever || platform == platATS || platform == platStartup,
		platAshby:      platform == platAll || platform == platAshby || platform == platATS || platform == platStartup,
		platYC:         platform == platAll || platform == platYC || platform == platStartup,
		platHN:         platform == platAll || platform == platHN || platform == platStartup,
		platIndeed:     platform == platAll || platform == platIndeed,
		platHabr:       platform == platAll || platform == platHabr,
		platTwitter:    platform == platAll || platform == platTwitter,
		platCraigslist: platform == platAll || platform == platCraigslist,
		platRemoteOK:   platform == platAll || platform == platRemoteOK || platform == platRemote,
		platWWR:        platform == platAll || platform == platWWR || platform == platRemote,
		platRemotive:   platform == platAll || platform == platRemotive || platform == platRemote,
		platFreelancer: platform == platAll || platform == platFreelancer,
		platGoogle:     platform == platAll || platform == platGoogle,
		// UN scrapers stay opt-in (NOT triggered by platform=all) — niche
		// international-org consultancies would otherwise crowd out generic
		// commercial searches.
		platInspira: platform == platInspira || platform == platUN,
		platUNDP:    platform == platUNDP || platform == platUN,
	}

	// Fixed order so output is deterministic (test stability + stable fan-out).
	order := []string{
		platLinkedIn, platGreenhouse, platLever, platAshby, platYC, platHN, platIndeed,
		platHabr, platTwitter, platCraigslist, platRemoteOK, platWWR, platRemotive,
		platFreelancer, platGoogle, platInspira, platUNDP,
	}
	srcs := make([]string, 0, len(order))
	for _, name := range order {
		if use[name] {
			srcs = append(srcs, name)
		}
	}
	return srcs
}

func buildJobSearxQuery(query, location, platform string) string {
	var sitePart string
	switch platform {
	case platLinkedIn:
		sitePart = "site:linkedin.com/jobs"
	case platGreenhouse:
		sitePart = "site:boards.greenhouse.io"
	case platLever:
		sitePart = "site:jobs.lever.co"
	case platAshby:
		sitePart = "site:jobs.ashbyhq.com"
	case platYC:
		sitePart = "site:workatastartup.com"
	case platHN:
		sitePart = "site:news.ycombinator.com \"who is hiring\""
	case platCraigslist:
		sitePart = "site:craigslist.org"
	case platRemoteOK:
		sitePart = "site:remoteok.com"
	case platWWR:
		sitePart = "site:weworkremotely.com"
	case platRemotive:
		sitePart = "site:remotive.com"
	case platRemote:
		sitePart = "site:remoteok.com OR site:weworkremotely.com OR site:remotive.com"
	case platFreelancer:
		sitePart = "site:freelancer.com/projects"
	case platGoogle:
		sitePart = "site:careers.google.com OR site:jobs.google.com"
	case platInspira:
		sitePart = "site:careers.un.org"
	case platUNDP:
		sitePart = "site:jobs.undp.org OR site:estm.fa.em2.oraclecloud.com"
	case platUN:
		sitePart = "site:careers.un.org OR site:jobs.undp.org"
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

// persistJobListings writes LLM-extracted job listings into the hunt store (best-effort).
func persistJobListings(ctx context.Context, jobListings []engine.JobListing) {
	store := engine.GetHuntStore()
	if store == nil {
		return
	}
	for _, j := range jobListings {
		if j.URL == "" {
			continue
		}
		_, outcome, err := store.UpsertJob(ctx, jobs.JobListingToHunt(j))
		engine.IncrHuntIngest(hunt.KindJob, outcome.String())
		if err != nil {
			slog.Warn("hunt: upsert job failed", slog.Any("error", err))
		}
	}
}
