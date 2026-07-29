package jobserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/anatolykoptev/go-kit/breaker"
	"github.com/anatolykoptev/go-kit/env"
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

// jobSearchSem bounds the number of connector goroutines that may run
// concurrently across ALL in-flight job_search requests (BH-1, #245).
// runSource() spawned 1 goroutine per connector (17-18 sources) per request
// with no cap; 10 concurrent job_search(all) calls = 180 goroutines → FD
// exhaustion and OOM. This package-level semaphore caps the fan-out at 8
// concurrent connector goroutines. Acquire is BLOCKING so every selected
// source still runs (the "all sources run" contract is preserved); only the
// concurrency is bounded, not the set of sources.
//
//nolint:gochecknoglobals // package-level concurrency cap, fixed at 8
var jobSearchSem = make(chan struct{}, 8)

//nolint:funlen // multi-platform aggregation
func registerJobSearch(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "job_search",
		Description: "Search for job listings on LinkedIn, Greenhouse, Lever, Ashby, YC workatastartup.com, HN Who is Hiring, Craigslist, RemoteOK, WeWorkRemotely, Remotive, Freelancer, Inspira (careers.un.org UN Secretariat), and UNDP (jobs.undp.org). Returns structured JSON with job details (title, company, location, salary, skills, URL) plus a `sources` array reporting the per-source outcome of the fan-out. Supports filters for experience level, job type, remote/onsite, time range, and platform. UN sources are opt-in: platform=inspira queries careers.un.org only, platform=undp queries jobs.undp.org only, platform=un fans out to both. The default platform=all DOES NOT query Inspira or UNDP — set platform explicitly when looking for UN-system openings. raw=true skips LLM processing and returns raw tweet objects — only meaningful when platform=twitter. The `sources` field (absent on cache hits and the twitter raw path) carries one SourceStatus per selected source with outcome ∈ {ok, empty, skipped, blocked, failed} and a reason: ok = ran and returned >=1 result; empty = ran and returned 0; skipped = never ran (missing API key); blocked = refused by upstream (breaker open, HTTP 403/429, bot challenge); failed = errored (transport, parse, deadline). When zero results coincide with any skipped/blocked/failed source, the summary names those sources instead of the generic 'No results found.'",
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

		// BH-1: Bound connector goroutine fan-out. Without a cap, 10 concurrent
		// job_search(platform=all) calls spawn 180 goroutines (18 sources × 10
		// requests), each holding a 90s timeout → FD exhaustion, OOM. Blocking
		// acquire preserves the "all sources run" contract, just bounds concurrency.
		for _, src := range srcs {
			jobSearchSem <- struct{}{}
			go func(s connectors.Source) {
				defer func() { <-jobSearchSem }()
				runSource(ctx, s, q, ch)
			}(src)
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
				// SearchWeb: go-search primary (Brave API + ox-browser + DDG via
				// proxy), SearchDirect fallback (local direct scrapers).
				results := engine.SearchWeb(ctx, searxQuery, lang)
				ch <- sourceResult{name: "searxng", results: results, err: nil}
			}()
		}

		totalGoroutines := len(srcs)
		if runGenericSearxng {
			totalGoroutines++
		}
		merged, linkedInJobs, sources := aggregateSourceResults(ctx, srcs, runGenericSearxng, ch, totalGoroutines)

		if len(merged) == 0 {
			return nil, engine.JobSearchOutput{
				Query:   input.Query,
				Summary: buildZeroResultsSummary(sources),
				Sources: sources,
			}, nil
		}

		// Partial-results path: the aggregation was cancelled mid-fan-out (the
		// ToolTimeoutMiddleware deadline fired, or the client disconnected). The
		// Sources list is already truthful (un-reported sources marked failed with
		// a deadline reason). Skip the LLM post-processing — it would either fail
		// on the cancelled context or burn the remaining budget — and return the
		// partial answer with an honest summary. This is a SUCCESS path, not an
		// error: a partial answer with a truthful Sources list beats a hang.
		if ctx.Err() != nil {
			return nil, engine.JobSearchOutput{
				Query:   input.Query,
				Summary: buildPartialSummary(sources),
				Sources: sources,
			}, nil
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
			return nil, engine.JobSearchOutput{Query: input.Query, Summary: "No more results (offset beyond total).", Sources: sources}, nil
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
			// Annotate each result with a deterministic quality score (no LLM).
			// Source is inferred from the URL when the listing has no Source field.
			if j.Source == "" {
				j.Source = extractSourceForQuality(j.URL)
			}
			j.QualityScore = qualityScoreFromListing(*j).Score
		}

		persistJobListings(ctx, jobOut.Jobs)
		jobOut.Sources = sources
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

// perSourceTimeout caps the wall-time of a single connector Fetch call.
// Set to JOB_SEARCH_TIMEOUT/2 (≈ 90s default) so even the slowest source
// (hn fan-out, inspira, etc.) cannot consume the whole MCP deadline; the
// remaining half is left for the other fan-out legs and LLM post-processing.
//
// Configurable via PER_SOURCE_TIMEOUT env (e.g. "60s"). A var (not const)
// so tests can shrink it to milliseconds; prod never reassigns it.
//
//nolint:gochecknoglobals // package-level config, init-once from env
var perSourceTimeout = getPerSourceTimeout()

func getPerSourceTimeout() time.Duration {
	return env.MustDuration("PER_SOURCE_TIMEOUT", 90*time.Second)
}

// runSource executes a single Source in a goroutine, recovering from panics so
// one broken connector cannot crash the entire fan-out. Records per-source
// duration into gojob_source_duration_seconds{platform} (ADR-J3, P3).
//
// Each source runs under a per-source deadline (perSourceTimeout, default 90s)
// derived independently from the parent context. This bounds slow sources
// (hn Algolia+Firebase fan-out, inspira careers.un.org) without relying on
// them implementing their own internal budgets (P4: hn/inspira timeout fix).
// Note: the cap is effective only when the source's HTTP calls propagate
// srcCtx to their requests. Sources that ignore context will run to completion
// regardless; the goroutine exits via context deadline only if it selects on
// ctx.Done() or uses a context-aware HTTP client.
func runSource(ctx context.Context, src connectors.Source, q connectors.Query, ch chan<- sourceResult) {
	// NeedsAPIKey gate: skip Fetch entirely when the required API key is absent.
	// Emits outcome=no_key directly, avoiding a doomed API round-trip.
	// The key check is cheap (config field read) so it runs before the per-source
	// deadline setup.
	if !connectors.HasRequiredAPIKey(src) {
		slog.Info("job_search: source skipped — API key not configured",
			slog.String("source", src.Name()))
		ch <- sourceResult{name: src.Name(), err: fmt.Errorf("%w: %s key not configured", jobs.ErrNoAPIKey, src.Name())}
		return
	}

	// Per-source deadline: independent of parent so a slow source does not
	// consume the whole MCP budget. If the parent ctx expires first, the
	// source still gets cancelled (WithTimeout inherits parent cancellation).
	srcCtx, srcCancel := context.WithTimeout(ctx, perSourceTimeout)
	defer srcCancel()

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
		results, liJobs, err := liFetcher.FetchRaw(srcCtx, q)
		engine.ObserveSourceDuration(src.Name(), time.Since(start).Seconds())
		if err != nil {
			slog.Warn("job_search: linkedin error", slog.Any("error", err))
		} else {
			slog.Info("job_search: linkedin returned jobs", slog.Int("count", len(liJobs)))
		}
		ch <- sourceResult{name: src.Name(), results: results, liJobs: liJobs, err: err}
		return
	}
	results, err := src.Fetch(srcCtx, q)
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

// huntNotifyOnSearch reads HUNT_NOTIFY_ON_SEARCH (default false).
// When false, persistJobListings is silent — the MCP caller sees results inline
// so a Telegram blast per result is redundant. Set to true to opt into inline
// notifications (e.g. when called from a background batch, not an interactive session).
func huntNotifyOnSearch() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("HUNT_NOTIFY_ON_SEARCH")), "true")
}

// persistJobListings writes LLM-extracted job listings into the hunt store (best-effort).
func persistJobListings(ctx context.Context, jobListings []engine.JobListing) {
	store := engine.GetHuntStore()
	if store == nil {
		return
	}
	notifyOnSearch := huntNotifyOnSearch()
	for _, j := range jobListings {
		if j.URL == "" {
			continue
		}
		hj := jobs.JobListingToHunt(j)
		_, outcome, err := store.UpsertJob(ctx, hj)
		engine.IncrHuntIngest(hunt.KindJob, outcome.String())
		if err != nil {
			slog.Warn("hunt: upsert job failed", slog.Any("error", err))
			continue
		}
		if notifyOnSearch && outcome == hunt.OutcomeCreated {
			// Inline notify: MCP caller opted in via HUNT_NOTIFY_ON_SEARCH=true.
			store.NotifyJobIfOpen(hj)
		}
	}
}

// allSourceNames returns the names of every fan-out participant in order:
// each selected connector followed by the generic searxng discovery goroutine
// when it runs. Used to mark un-reported sources as failed on context
// cancellation.
func allSourceNames(srcs []connectors.Source, runGenericSearxng bool) []string {
	names := make([]string, 0, len(srcs)+1)
	for _, s := range srcs {
		names = append(names, s.Name())
	}
	if runGenericSearxng {
		names = append(names, "searxng")
	}
	return names
}

// aggregateSourceResults drains the fan-out channel, classifying each source
// return into a SourceStatus, until every goroutine has reported OR the context
// is cancelled. On cancellation it returns the partial results collected so far
// and marks every source that has not yet reported as failed with a deadline
// reason — so the response stays truthful instead of hanging on a bare
// `r := <-ch` that ignores ctx.Done().
//
// The per-platform results counter (gojob_platform_results_total) is bumped
// once per connector return here, exactly as the inline loop did before.
func aggregateSourceResults(
	ctx context.Context,
	srcs []connectors.Source,
	runGenericSearxng bool,
	ch <-chan sourceResult,
	totalGoroutines int,
) (merged []engine.SearxngResult, linkedInJobs []jobs.LinkedInJob, sources []engine.SourceStatus) {
	expected := allSourceNames(srcs, runGenericSearxng)
	reported := make(map[string]bool, len(expected))
	received := 0
loop:
	for received < totalGoroutines {
		select {
		case r := <-ch:
			received++
			reported[r.name] = true
			engine.IncrPlatformResults(r.name, engine.PlatformOutcome(len(r.results), r.err))
			merged = append(merged, r.results...)
			if r.name == platLinkedIn && len(r.liJobs) > 0 {
				linkedInJobs = r.liJobs
			}
			sources = append(sources, classifySourceResult(r))
		case <-ctx.Done():
			// Partial: mark every expected source that has not reported as failed
			// with a deadline reason. Sources that DID report (even with a
			// cancellation error) were already classified above and are not
			// double-counted.
			for _, name := range expected {
				if !reported[name] {
					sources = append(sources, engine.SourceStatus{
						Name:    name,
						Outcome: engine.SourceOutcomeFailed,
						Reason:  "deadline: source did not report before context cancellation",
					})
				}
			}
			break loop
		}
	}
	return merged, linkedInJobs, sources
}

// classifySourceResult maps a single sourceResult into the response-contract
// SourceStatus vocabulary. Classification precedence:
//   - nil err + results  -> ok
//   - nil err + 0 results -> empty (genuine zero — the connector ran and succeeded)
//   - errors.Is(ErrNoAPIKey) -> skipped (never ran: missing key / opted out)
//   - errors.Is(breaker.ErrOpen) -> blocked (upstream refused: breaker open)
//   - context deadline/cancellation -> failed (deadline)
//   - any other error -> failed (transport / parse / unknown)
//
// Note: several connectors historically returned (nil, nil) on failure — that
// is the bug this classification makes visible as "empty" rather than
// inheriting. A genuine empty (ran, 0 results) and a masked failure (nil, nil)
// are indistinguishable at the sourceResult level; the connector-specific
// remediation is out of scope for this task. The Sources list at least makes
// the per-source count + outcome visible to the caller.
func classifySourceResult(r sourceResult) engine.SourceStatus {
	st := engine.SourceStatus{Name: r.name, Count: len(r.results)}
	switch {
	case r.err == nil && len(r.results) > 0:
		st.Outcome = engine.SourceOutcomeOK
	case r.err == nil:
		st.Outcome = engine.SourceOutcomeEmpty
	case errors.Is(r.err, jobs.ErrNoAPIKey):
		st.Outcome = engine.SourceOutcomeSkipped
		st.Reason = r.err.Error()
	case errors.Is(r.err, breaker.ErrOpen):
		st.Outcome = engine.SourceOutcomeBlocked
		st.Reason = r.err.Error()
	case errors.Is(r.err, context.DeadlineExceeded), errors.Is(r.err, context.Canceled):
		st.Outcome = engine.SourceOutcomeFailed
		st.Reason = "deadline: " + r.err.Error()
	default:
		st.Outcome = engine.SourceOutcomeFailed
		st.Reason = r.err.Error()
	}
	return st
}

// buildZeroResultsSummary produces the human-readable summary for the
// zero-results branch. "No results found." is only correct when every selected
// source reported ok or empty. If any source was skipped / blocked / failed,
// the summary names those sources (and their outcome) so the caller can tell
// "there are no such jobs" apart from "the source never ran / was refused /
// errored" — the core contract this task fixes.
func buildZeroResultsSummary(sources []engine.SourceStatus) string {
	var problems []engine.SourceStatus
	for _, s := range sources {
		switch s.Outcome {
		case engine.SourceOutcomeSkipped, engine.SourceOutcomeBlocked, engine.SourceOutcomeFailed:
			problems = append(problems, s)
		}
	}
	if len(problems) == 0 {
		return "No results found."
	}
	parts := make([]string, 0, len(problems))
	for _, p := range problems {
		reason := p.Reason
		if reason == "" {
			reason = p.Outcome
		}
		parts = append(parts, fmt.Sprintf("%s (%s: %s)", p.Name, p.Outcome, reason))
	}
	return "No results found — sources did not complete successfully: " + strings.Join(parts, "; ") + "."
}

// buildPartialSummary produces the summary for the partial-results-on-cancellation
// path. It names the deadline and the sources that did not finish.
func buildPartialSummary(sources []engine.SourceStatus) string {
	var unfinished []string
	for _, s := range sources {
		if s.Outcome == engine.SourceOutcomeFailed {
			unfinished = append(unfinished, s.Name)
		}
	}
	if len(unfinished) == 0 {
		return "Partial results — search deadline reached."
	}
	return "Partial results — search deadline reached; sources did not finish: " + strings.Join(unfinished, ", ") + "."
}
