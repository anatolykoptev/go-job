package jobserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/anatolykoptev/go_job/internal/engine/jobs/connectors"
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

// sourceResult carries the output of a single connector goroutine.
type sourceResult struct {
	name    string
	results []engine.SearxngResult
	liJobs  []jobs.LinkedInJob
	err     error
}

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
		// Unknown/typo'd platform (e.g. "greehouse") would otherwise route to NO
		// connector AND suppress the generic searxng goroutine → a guaranteed
		// "No results found." Fall back to platAll (broad search) and warn, so a
		// typo degrades to results rather than silence (reviewer MINOR).
		if !knownPlatform(platform) {
			slog.Warn("job_search: unknown platform, falling back to all",
				slog.String("platform", platform))
			platform = platAll
		}

		limit := input.Limit
		if limit <= 0 {
			limit = 15
		}
		if limit > 50 {
			limit = 50
		}

		srcs := jobRegistry.Select(platform)
		q := connectors.Query{
			Query:      input.Query,
			Location:   input.Location,
			Experience: input.Experience,
			JobType:    input.JobType,
			Remote:     input.Remote,
			TimeRange:  input.TimeRange,
			Salary:     input.Salary,
			Limit:      limit,
			Offset:     input.Offset,
			EasyApply:  input.EasyApply,
			Language:   lang,
		}

		ch := make(chan sourceResult, len(srcs)+1)

		for _, src := range srcs {
			go runSource(ctx, src, q, ch)
		}

		// Generic web-search discovery (go-engine DIRECT + SearXNG) is broad and
		// only appropriate for platform=all. When the caller asks for a SPECIFIC
		// connector (greenhouse/lever/ashby/yc/indeed/…), this goroutine's
		// engine-wide web search surfaces stale Wikipedia/Marginalia hits (2017-
		// 2021) that masquerade as ATS results — exactly the discovery-collapse
		// symptom (2026-06-23, H3). Gate it off for specific platforms so the
		// merged output reflects only the chosen connector's structured results.
		runGenericSearxng := shouldRunGenericSearxng(platform)
		if runGenericSearxng {
			go func() {
				var searxQuery string
				if input.Location != "" {
					searxQuery = input.Query + " " + input.Location + " jobs"
				} else {
					searxQuery = input.Query + " jobs"
				}
				// go-engine DIRECT (primary, always-on via DIRECT_* env) + SearXNG (additive).
				results := engine.SearchDirect(ctx, searxQuery, lang)
				searx, err := engine.SearchSearXNG(ctx, searxQuery, lang, input.TimeRange, engine.DefaultSearchEngine)
				if err != nil {
					slog.Warn("job_search: searxng error (additive)", slog.Any("error", err))
				}
				results = append(results, searx...)
				// DIRECT is authoritative; additive SearXNG err is intentionally not
				// propagated — the result set reflects what DIRECT returned regardless.
				ch <- sourceResult{name: "searxng", results: results, err: nil}
			}()
		}

		totalGoroutines := len(srcs)
		if runGenericSearxng {
			totalGoroutines++
		}
		var merged []engine.SearxngResult
		var linkedInJobs []jobs.LinkedInJob
		for i := 0; i < totalGoroutines; i++ {
			r := <-ch
			// Unified per-platform counter: bumped once per connector return, covering
			// all 18 platforms uniformly. Per-connector bumps were removed to avoid
			// double-counting; this is the single authority for platform_results_total.
			engine.IncrPlatformResults(r.name, engine.PlatformOutcome(len(r.results), r.err))
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

// knownPlatform reports whether platform is a recognized job_search platform.
// Delegates to the registry which encodes known connector names + group names.
func knownPlatform(platform string) bool { return jobRegistry.Known(platform) }

// shouldRunGenericSearxng decides whether the always-on generic web-search
// discovery goroutine (go-engine DIRECT + SearXNG) runs alongside the selected
// connectors. It runs ONLY for platform=all. For a specific connector the
// generic web search surfaces stale Wikipedia/Marginalia hits that masquerade
// as ATS results (discovery-collapse H3, 2026-06-23) — so it is suppressed and
// the merged output reflects only the chosen connector's structured results.
func shouldRunGenericSearxng(platform string) bool {
	return platform == platAll
}

// runSource executes a single Source in a goroutine, recovering from panics so
// one broken connector cannot crash the entire fan-out. Records per-source
// duration into gojob_source_duration_seconds{platform} (ADR-J3, P3).
func runSource(ctx context.Context, src connectors.Source, q connectors.Query, ch chan<- sourceResult) {
	start := time.Now()
	defer func() {
		if r := recover(); r != nil {
			engine.ObserveSourceDuration(src.Name(), time.Since(start).Seconds())
			slog.Error("job_search: source panicked",
				slog.String("source", src.Name()),
				slog.Any("recover", r))
			ch <- sourceResult{name: src.Name(), err: fmt.Errorf("panic: %v", r)}
		}
	}()
	if liFetcher, ok := src.(connectors.RawLinkedInFetcher); ok {
		results, liJobs, err := liFetcher.FetchRaw(ctx, q)
		engine.ObserveSourceDuration(src.Name(), time.Since(start).Seconds())
		if err != nil {
			slog.Warn("job_search: linkedin error", slog.Any("error", err))
		} else {
			slog.Info("job_search: linkedin returned jobs", slog.Int("count", len(liJobs)))
		}
		ch <- sourceResult{name: src.Name(), results: results, liJobs: liJobs, err: err}
		return
	}
	results, err := src.Fetch(ctx, q)
	engine.ObserveSourceDuration(src.Name(), time.Since(start).Seconds())
	if err != nil {
		slog.Warn("job_search: source error",
			slog.String("source", src.Name()),
			slog.Any("error", err))
	}
	ch <- sourceResult{name: src.Name(), results: results, err: err}
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
