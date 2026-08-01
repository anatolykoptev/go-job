package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/anatolykoptev/go-kit/breaker"
	"github.com/anatolykoptev/go-kit/env"
	"github.com/anatolykoptev/go-kit/ratelimit"
	"github.com/anatolykoptev/go_job/internal/engine"
)

// atsLimiter caps concurrent outbound ATS API calls across all providers.
// Configurable via GO_JOB_ATS_MAX_CONCURRENT env (default 3).
//
//nolint:gochecknoglobals // package-level limiter, mirrors atsBreakers pattern below
var atsLimiter = ratelimit.NewConcurrencyLimiter(getATSMaxConcurrent())

// ATSDiscoverer is the optional cross-service URL-discovery backend.
// When non-nil, discoverJobURLs tries it first (go-search fused multi-source
// path); a clean (no-error) empty answer is trusted and local scrapers are
// NOT consulted — local fallback fires only on transport/decode error.
// When nil, local SearchDirect is the only path (preserves current behaviour).
// Set via SetATSDiscoverer; read by discoverJobURLs.
//
//nolint:gochecknoglobals // package-level singleton, set once at startup, read-only after
var ATSDiscoverer discoverer

// discoverer is the local interface that ats.go depends on.
// Mirrors discovery.Discoverer — defined here to avoid a package-level import
// cycle (discovery imports engine; jobs imports engine too; jobs must not
// import discovery which in turn imports engine again).
type discoverer interface {
	DiscoverBoardURLs(ctx context.Context, query string) ([]engine.SearxngResult, error)
}

// SetATSDiscoverer wires the cross-service discovery client at startup.
// Idempotent; calling with nil reverts to local-only mode.
func SetATSDiscoverer(d discoverer) { ATSDiscoverer = d }

// searchWeb is the unified web-search helper for person/salary/company research.
// Delegates to engine.SearchWeb (go-search primary, SearchDirect fallback).
func searchWeb(ctx context.Context, query string) []engine.SearxngResult {
	return engine.SearchWeb(ctx, query, "all")
}

func getATSMaxConcurrent() int {
	return env.MustInt("GO_JOB_ATS_MAX_CONCURRENT", 3)
}

// getDiscoveryQueryVariants returns the number of query variants to fan out
// per platform in unionDiscoverSlugs. Configurable via DISCOVERY_QUERY_VARIANTS
// (range 1–5, default 3).
func getDiscoveryQueryVariants() int {
	return env.MustInt("DISCOVERY_QUERY_VARIANTS", 3)
}

// Per-provider circuit breakers. After FailThreshold consecutive failures the
// breaker opens for OpenDuration, blocking further attempts. After cooldown it
// half-opens for one probe; if that succeeds the breaker resets to closed.
//
// #180 fix: All three breakers now have BackoffMultiplier + MaxOpenDuration
// so a permanently-down ATS API doesn't disable discovery indefinitely —
// the breaker backs off exponentially up to MaxOpenDuration (5m), then
// half-opens for a probe on every cycle, so recovery is automatic.
//
//nolint:gochecknoglobals // package-level breakers, init-once, never mutated
var (
	ashbyBreaker = breaker.New(breaker.Options{
		Name:              "ashby",
		FailThreshold:     3,
		OpenDuration:      30 * time.Second,
		BackoffMultiplier: 2.0,
		MaxOpenDuration:   5 * time.Minute,
	})
	greenhouseBreaker = breaker.New(breaker.Options{
		Name:              "greenhouse",
		FailThreshold:     3,
		OpenDuration:      30 * time.Second,
		BackoffMultiplier: 2.0,
		MaxOpenDuration:   5 * time.Minute,
	})
	leverBreaker = breaker.New(breaker.Options{
		Name:              "lever",
		FailThreshold:     3,
		OpenDuration:      30 * time.Second,
		BackoffMultiplier: 2.0,
		MaxOpenDuration:   5 * time.Minute,
	})
)

// discoverJobURLs returns URL/title pairs for ATS slug extraction.
//
// When ATSDiscoverer is set (GO_SEARCH_URL configured at startup), it calls the
// go-search fused multi-source pipeline (Brave-API + ox-browser-search + DDG,
// RRF-fused) as the PRIMARY path — the only DC-reliable sources that index
// boards.greenhouse.io, jobs.lever.co, and jobs.ashbyhq.com (ADR-002, 2026-06-23).
//
// go-search is treated as AUTHORITATIVE when reachable:
//   - clean (no-error) empty answer → trusted empty, local scrapers NOT consulted.
//     Callers that chain multiple discovery queries (e.g. lever dual-query) depend
//     on reliable empty-on-miss semantics to know when to try a secondary query.
//   - Degraded=true (engine.ErrDiscoveryDegraded wrapped in err) → partial fan-out
//     failure on go-search's side; fall through to local scrapers, observable as
//     gojob_hunt_discovery_source_total{source="degraded-fallback"}.
//   - transport / connection error → fall through to local scrapers, observable as
//     gojob_hunt_discovery_source_total{source="local-fallback"}.
//
// "degraded-fallback" and "local-fallback" are kept DISTINCT so operators can
// tell apart "go-search returned a partial answer" (degraded-fallback, go-search
// was reachable but its scrapers failed) from "go-search was unreachable"
// (local-fallback, network/timeout before any response).
//
// When ATSDiscoverer is nil the local path runs unconditionally (no metric bump).
func discoverJobURLs(ctx context.Context, query string) []engine.SearxngResult {
	if ATSDiscoverer != nil {
		results, err := ATSDiscoverer.DiscoverBoardURLs(ctx, query)
		if err != nil {
			if errors.Is(err, engine.ErrDiscoveryDegraded) {
				// go-search returned 200+Degraded=true: partial source failure.
				// Fall through to local scrapers with a distinct metric label so
				// dashboards can separate this from a transport error.
				slog.Warn("discover: go-search degraded, falling back to local",
					slog.String("query", query),
					slog.Any("error", err),
				)
				engine.IncrHuntDiscoverySource("degraded-fallback")
			} else {
				// Transport/connection/decode error — go-search was unreachable.
				slog.Warn("discover: go-search unavailable, falling back to local",
					slog.String("query", query),
					slog.Any("error", err),
				)
				engine.IncrHuntDiscoverySource("local-fallback")
			}
		} else {
			// go-search returned a definitive answer (empty or not): trust it and
			// skip local scrapers.  Local fallback runs only on transport errors so
			// callers that chain multiple queries (e.g. lever dual-query) get correct
			// empty-on-miss semantics instead of stale local results.
			engine.IncrHuntDiscoverySource("go-search")
			return deduplicateByURL(results)
		}
		// Fall through to local path only on error.
	}

	// Local path: used when ATSDiscoverer is nil or returned a transport error.
	direct := engine.SearchDirect(ctx, query, "all")
	return deduplicateByURL(direct)
}

// deduplicateByURL deduplicates SearxngResult by URL, preserving order.
func deduplicateByURL(in []engine.SearxngResult) []engine.SearxngResult {
	seen := make(map[string]bool, len(in))
	merged := make([]engine.SearxngResult, 0, len(in))
	for _, r := range in {
		if r.URL == "" || seen[r.URL] {
			continue
		}
		seen[r.URL] = true
		merged = append(merged, r)
	}
	return merged
}

// discoveryVariants returns up to getDiscoveryQueryVariants() distinct query
// strings for the given platform, base role query, and optional location.
// Each variant orders the query tokens differently to exploit different search
// engine ranking surfaces. Returns nil for unknown platforms.
func discoveryVariants(platform, base, location string) []string {
	n := getDiscoveryQueryVariants()

	var scope string
	switch platform {
	case engine.DiscoveryPlatformGreenhouse:
		scope = greenhouseSiteSearch
	case engine.DiscoveryPlatformLever:
		scope = leverSiteSearch
	case engine.DiscoveryPlatformAshby:
		scope = ashbySiteSearch
	default:
		return nil
	}

	base = strings.TrimSpace(base)
	loc := strings.TrimSpace(location)

	templates := []string{
		buildDiscoveryV1(base, loc, scope),
		buildDiscoveryV2(base, loc, scope),
		buildDiscoveryV3(base, loc, scope),
	}

	if n < len(templates) {
		templates = templates[:n]
	}
	return templates
}

func buildDiscoveryV1(base, loc, scope string) string {
	if loc != "" {
		return base + " " + loc + " " + scope
	}
	return base + " " + scope
}

func buildDiscoveryV2(base, loc, scope string) string {
	if loc != "" {
		return scope + " " + base + " " + loc
	}
	return scope + " " + base
}

func buildDiscoveryV3(base, loc, scope string) string {
	if loc != "" {
		return "senior " + base + " " + scope + " " + loc
	}
	return "senior " + base + " " + scope
}

// unionDiscoverSlugs fans out DISCOVERY_QUERY_VARIANTS queries for the given
// platform in parallel, extracts slugs from each, unions fresh results with
// the runtime slug cache, and returns the deduplicated union (capped at
// maxATSSlugsPerSearch).
//
// extractFn is the platform-specific slug extractor (e.g. extractLeverSlugs).
// Called by SearchGreenhouseJobs, SearchLeverJobs, SearchAshbyJobs.
func unionDiscoverSlugs(
	ctx context.Context,
	platform, base, location string,
	extractFn func([]engine.SearxngResult) []string,
) []string {
	variants := discoveryVariants(platform, base, location)

	sc := GetSlugCache()
	var cached []string
	if sc != nil {
		cached = sc.Get(platform)
	}

	if len(variants) == 0 {
		return cached
	}

	type varResult struct {
		slugs []string
		hit   bool
	}
	ch := make(chan varResult, len(variants))
	for _, v := range variants {
		v := v
		go func() {
			urls := discoverJobURLs(ctx, v)
			slugs := extractFn(urls)
			ch <- varResult{slugs: slugs, hit: len(slugs) > 0}
		}()
	}

	freshSeen := make(map[string]bool)
	var fresh []string
	for range variants {
		// BH-6: check ctx.Done() so the receive loop exits early on
		// cancellation — goroutines sending to ch will block on the buffered
		// channel but the caller no longer waits for all of them.
		select {
		case <-ctx.Done():
			// Context cancelled: return just the cached slugs (no fresh
			// results from the cancelled discovery goroutines).
			return cached
		case r := <-ch:
			result := "miss"
			if r.hit {
				result = "hit"
			}
			engine.IncrHuntDiscoveryVariant(platform, result)
			for _, s := range r.slugs {
				if !freshSeen[s] {
					freshSeen[s] = true
					fresh = append(fresh, s)
				}
			}
		}
	}

	cachedSeen := make(map[string]bool, len(cached))
	union := make([]string, 0, len(cached)+len(fresh))
	for _, s := range cached {
		if !cachedSeen[s] {
			cachedSeen[s] = true
			union = append(union, s)
		}
	}
	for _, s := range fresh {
		if !cachedSeen[s] {
			cachedSeen[s] = true
			union = append(union, s)
		}
	}

	if sc != nil && len(fresh) > 0 {
		sc.Merge(ctx, platform, fresh)
	}

	if len(union) > maxATSSlugsPerSearch {
		union = union[:maxATSSlugsPerSearch]
	}
	return union
}

// atsBoardMaxBytes is the hard DoS-ceiling for ATS board responses across all
// three fetchers (greenhouse, lever, ashby). The cap is enforced via a
// io.LimitReader wrapping the response body, but the body is STREAM-DECODED
// (json.NewDecoder.Decode) rather than ReadAll'd, so this constant is a safety
// ceiling — not the expected working size. Boards under the cap consume only
// as much RAM as the JSON decoder needs (incremental), not the full payload.
//
// 64 MiB chosen as: 4× the live worst-case (insiderone lever ≈ 16 MB sales-heavy
// board per P6 incident) while remaining well below the 24 GB box RAM budget even
// with N parallel union-fetches.  The old 16 MiB cap caused the P6 counter hit
// (fetch_errors{lever,reason=truncated}=1) on large sales-heavy lever boards.
//
// Truncation detection: if json.Decode returns an error AND the countingReader
// consumed >= cap bytes, the LimitReader hit the ceiling mid-decode →
// ErrBodyTruncated (visible as reason=truncated). Any other decode error →
// reason=parse.
//
// Declared as var (not const) to allow test substitution (same pattern as
// leverAPIBase / greenhouseBoardsAPI); production value is never mutated.
//
//nolint:gochecknoglobals // var (not const) to allow test substitution
var atsBoardMaxBytes int64 = 64 * 1024 * 1024 // 64 MiB DoS ceiling

// atsBoardDecode stream-decodes a JSON board response from r into target.
// Wraps r in an atsBoardMaxBytes LimitReader (DoS ceiling). Returns
// ErrBodyTruncated if the cap was hit mid-decode; a decode error otherwise.
//
// Memory: incremental — no full-body buffer. Peak allocation is O(one JSON token).
func atsBoardDecode(r io.Reader, target any) error {
	return atsBoardDecodeWithCap(r, atsBoardMaxBytes, target)
}

// atsBoardDecodeWithCap is the cap-parameterised implementation used by
// atsBoardDecode and directly by unit tests (which use a small cap to avoid
// allocating a 64 MB fixture).
func atsBoardDecodeWithCap(r io.Reader, cap int64, target any) error {
	cr := &countingReader{r: r}
	lr := io.LimitReader(cr, cap)
	dec := json.NewDecoder(lr)
	if err := dec.Decode(target); err != nil {
		// If consumed >= cap the LimitReader hit the ceiling mid-stream →
		// the decoder received an unexpected EOF. Surface as ErrBodyTruncated.
		if cr.n >= cap {
			return ErrBodyTruncated
		}
		return err
	}
	return nil
}

// countingReader wraps an io.Reader and tracks total bytes read.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// --- Greenhouse ---

//nolint:gochecknoglobals // var (not const) to allow test substitution
var greenhouseBoardsAPI = "https://boards-api.greenhouse.io/v1/boards/%s/jobs"

const greenhouseSiteSearch = "site:boards.greenhouse.io"

// maxATSSlugsPerSearch caps how many company slugs we fan-out to per ATS source per query.
const maxATSSlugsPerSearch = 5

// greenhouseSlugRe extracts company slug from boards.greenhouse.io URLs.
var greenhouseSlugRe = regexp.MustCompile(`boards\.greenhouse\.io/([^/?#]+)`)

// greenhouseJob is a single job from the Greenhouse public API.
type greenhouseJob struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Location struct {
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

// greenhouseJobToListing builds a structured engine.JobListing from a parsed
// Greenhouse job, the board slug (company), and the resolved job URL. Populates
// every field the Greenhouse API carries that JobListing has a slot for
// (title, location.name, absolute_url, updated_at, content, company←board
// slug). departments[].name is parsed by greenhouseJob but NOT mapped —
// engine.JobListing has no departments field today; the fixture carries it
// so a future mapping has the data ready. Skills and Experience stay empty —
// Greenhouse carries neither. Description mirrors the 600-rune truncation the
// SearxngResult content string uses, so the structured field and the
// LLM-facing snippet stay consistent.
func greenhouseJobToListing(job greenhouseJob, slug, jobURL string) engine.JobListing {
	desc := ""
	if job.Content != "" {
		desc = engine.TruncateRunes(engine.CleanHTML(job.Content), 600, "...")
	}
	return engine.JobListing{
		Title:       job.Title,
		Company:     slug,
		URL:         jobURL,
		JobID:       strconv.FormatInt(job.ID, 10),
		Source:      string(PlatformGreenhouse),
		Location:    job.Location.Name,
		Description: desc,
		Posted:      job.UpdatedAt,
	}
}

// metaKeyPostedAt is the SearxngResult.Metadata key carrying the structured ATS
// posting date (RFC3339). SearxngResultToHuntJob reads this back into
// hunt.Job.PostedAt without depending on the lossy LLM "posted"-field round-trip
// (the content string drops the date for lever entirely and labels it
// inconsistently for greenhouse/ashby). The value is the ATS API field verbatim
// for ISO sources (greenhouse updated_at, ashby publishedAt) or the epoch-ms
// conversion for lever createdAt.
const metaKeyPostedAt = "posted_at"

// leverCreatedAtToISO converts a Lever createdAt epoch-millisecond timestamp to an
// RFC3339 string. Returns "" for non-positive input (field absent / zero), mirroring
// the opire.go bounty-posted conversion. Lever's API delivers createdAt as epoch ms,
// not seconds, so time.UnixMilli is the correct constructor.
func leverCreatedAtToISO(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// setPostedAtMeta stores posted (an RFC3339 / ISO date string from the ATS API) into
// r.Metadata under metaKeyPostedAt, lazily allocating the map. Empty posted is a
// no-op so absent dates stay absent (SearxngResultToHuntJob then yields a nil
// PostedAt → NULL posted_at, the pre-fix behaviour for that single row).
func setPostedAtMeta(r *engine.SearxngResult, posted string) {
	if posted == "" {
		return
	}
	if r.Metadata == nil {
		r.Metadata = make(map[string]string, 1)
	}
	r.Metadata[metaKeyPostedAt] = posted
}

// SearchGreenhouseJobs discovers company slugs via multi-query union (P1) +
// runtime slug cache (P2), then hits the public JSON API for each slug.
// Returns only the SearxngResult slice (web-search-result shape) for consumers
// that do not need the structured fields (hunt worker, Source.Fetch). The
// structured JobListing collection is discarded here; use
// SearchGreenhouseJobsStructured to obtain it.
func SearchGreenhouseJobs(ctx context.Context, query, location string, limit int) ([]engine.SearxngResult, error) {
	results, _, err := SearchGreenhouseJobsStructured(ctx, query, location, limit)
	return results, err
}

// SearchGreenhouseJobsStructured is the canonical implementation: it returns
// both the SearxngResult slice (unchanged shape, consumed by the ranking /
// dedup / blacklist helpers) AND a parallel []engine.JobListing keyed by the
// same URL, carrying every structured field the Greenhouse API exposed. The two
// slices are appended in lockstep so index i in results shares its URL with
// index i in listings.
func SearchGreenhouseJobsStructured(ctx context.Context, query, location string, limit int) ([]engine.SearxngResult, []engine.JobListing, error) {
	engine.IncrGreenhouseRequests()

	slugs := unionDiscoverSlugs(ctx, engine.DiscoveryPlatformGreenhouse, query, location, extractGreenhouseSlugs)
	engine.IncrHuntDiscoveryURLs(engine.DiscoveryPlatformGreenhouse, len(slugs))
	if len(slugs) == 0 {
		slog.Debug("greenhouse: no slugs found in discovery results")
		return nil, nil, nil
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
			// BH-13: per-fetch timeout so one slow slug doesn't block the
			// entire platform's result collection until perPlatformTimeout.
			fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			jobs, err := fetchGreenhouseJobs(fetchCtx, s)
			ch <- fetchResult{s, jobs, err}
		}(slug)
	}

	keywords := strings.Fields(strings.ToLower(query))
	var allResults []engine.SearxngResult
	var allListings []engine.JobListing
	for i := 0; i < len(slugs); i++ {
		r := <-ch
		if r.err != nil {
			slog.Warn("greenhouse: fetch error", slog.String("slug", r.slug), slog.Any("error", r.err))
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
			sr := engine.SearxngResult{
				Title:   job.Title,
				Content: content,
				URL:     jobURL,
				Score:   0.9,
			}
			setPostedAtMeta(&sr, job.UpdatedAt) // greenhouse updated_at is RFC3339 ISO
			allResults = append(allResults, sr)
			allListings = append(allListings, greenhouseJobToListing(job, r.slug, jobURL))
			if len(allResults) >= limit {
				break
			}
		}
		if len(allResults) >= limit {
			break
		}
	}

	slog.Debug("greenhouse: search complete", slog.Int("results", len(allResults)))
	return allResults, allListings, nil
}

// fetchGreenhouseJobs fetches all jobs for a given company slug.
//
//nolint:dupl // structurally similar to fetchAshbyJobs/fetchLeverPostings — different types, URLs, body limits
func fetchGreenhouseJobs(ctx context.Context, slug string) (jobs []greenhouseJob, err error) {
	if !greenhouseBreaker.Allow() {
		engine.IncrATSBreakerOpen()
		return nil, fmt.Errorf("greenhouse breaker open: %w", breaker.ErrOpen)
	}
	defer func() { greenhouseBreaker.Record(err == nil) }()

	release, err := atsLimiter.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	apiURL := fmt.Sprintf(greenhouseBoardsAPI, slug)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentBot)
	req.Header.Set("Accept", "application/json")

	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		return engine.Cfg.HTTPClient.Do(req) //nolint:gosec // ATS API URL from argument, intentional outbound request
	})
	if err != nil {
		engine.IncrATSFetchErrors(engine.DiscoveryPlatformGreenhouse, "transport")
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		if sc := GetSlugCache(); sc != nil {
			sc.Evict(engine.DiscoveryPlatformGreenhouse, slug, "board_404")
		}
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		engine.IncrATSFetchErrors(engine.DiscoveryPlatformGreenhouse, "status")
		return nil, fmt.Errorf("greenhouse API status %d for %s", resp.StatusCode, slug)
	}

	var gr greenhouseResponse
	if err := atsBoardDecode(resp.Body, &gr); err != nil {
		if isBodyTruncated(err) {
			engine.IncrATSFetchErrors(engine.DiscoveryPlatformGreenhouse, "truncated")
			return nil, fmt.Errorf("greenhouse %s: %w", slug, ErrBodyTruncated)
		}
		engine.IncrATSFetchErrors(engine.DiscoveryPlatformGreenhouse, "parse")
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

//nolint:gochecknoglobals // var (not const) to allow test substitution
var leverAPIBase = "https://api.lever.co/v0/postings/%s?mode=json"

const leverSiteSearch = "site:jobs.lever.co"

// leverSlugRe extracts company slug from jobs.lever.co URLs.
var leverSlugRe = regexp.MustCompile(`jobs\.lever\.co/([^/?#]+)`)

// leverPosting is a single job from the Lever public API.
type leverPosting struct {
	ID         string `json:"id"`
	Text       string `json:"text"`
	HostedURL  string `json:"hostedUrl"`
	ApplyURL   string `json:"applyUrl"`
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
		Interval string `json:"interval"` // Lever: "per-year-salary" | "per-hour-wage" | "per-month-salary" | ...
	} `json:"salaryRange"`
	CreatedAt        int64  `json:"createdAt"`
	DescriptionPlain string `json:"descriptionPlain"`
	WorkplaceType    string `json:"workplaceType"`
	Country          string `json:"country"`
}

// normalizeRemote maps a provider's workplace-type vocabulary to the
// prompt_jobs.go contract vocabulary: remote | hybrid | onsite | "".
// Returns "" for empty/unspecified so the LLM value survives precedence
// rather than being overwritten with nothing. Provider vocabularies:
//   - Lever workplaceType: unspecified | on-site | remote | hybrid
//   - Ashby workplaceType:  remote | hybrid | onsite (casing may vary)
//
// The hunt_jobs.remote column is filtered by EXACT equality (hunt/store.go:542:
// remote = $N), so "on-site" (Lever's hyphenated form) MUST become "onsite"
// or hunt_list remote=onsite returns zero Lever rows. Similarly, "unspecified"
// MUST become "" so it does not clobber a real LLM-normalized value via
// FillStructuredFromLLM.
func normalizeRemote(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "remote": //nolint:goconst // semantic: JobListing.Remote vocabulary, distinct from algoraRemoteRow
		return "remote"
	case "hybrid": //nolint:goconst // semantic: JobListing.Remote vocabulary
		return "hybrid"
	case "on-site", "onsite", "on_site":
		return "onsite"
	default: // "", "unspecified", anything else
		return ""
	}
}

// normalizeSalaryInterval maps Lever's interval vocabulary to the
// types_jobs.go contract: "year" | "hour" | "month". Lever emits
// "per-year-salary", "per-hour-wage", and "per-month-salary"; these map to
// "year", "hour", and "month". An unrecognized/empty interval returns "" —
// callers MUST then leave SalaryMin/SalaryMax nil rather than asserting
// annual, so a per-hour posting is not scored as annual (BLOCKER D: the scorer
// at hunt/score/scorer.go:370 renders SalaryMin/Max into a prompt whose next
// line says "Minimum compensation: $X USD total comp"; $80/hour scored as
// $80/year is the traced failure).
//
// Unmapped intervals (per-week-salary, per-day-wage, one-time) return "" —
// the contract has no week/day/one-time bucket, and asserting annual would
// mis-score them. leverPostingToListing still leaves SalaryInterval empty for
// these (it will not assert a contract bucket it cannot map), but
// leverSalaryString now renders the RAW interval token verbatim so the number
// reaches both consumers carrying its own disambiguation — see leverSalaryString.
func normalizeSalaryInterval(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "per-year-salary", "year", "annual": //nolint:goconst // semantic: SalaryInterval vocabulary
		return "year"
	case "per-hour-wage", "hour", "hourly":
		return "hour"
	case "per-month-salary", "month", "monthly":
		return "month"
	default:
		return ""
	}
}

// leverSalaryString renders a leverPosting's salaryRange as a human-readable
// string via formatSalary (reused from tracker.go — the fourth copy of salary
// formatting in this file was the REUSE finding). Returns "" when neither min
// nor max is positive, or when there is no interval information at all.
//
// Two call sites: SearchLeverJobsStructured (the LLM-facing SearxngResult
// content string) and FetchATSBoard (the operator-facing ATSJob.Compensation
// field). The second never reaches precedence or the scorer, so the old
// "suppress so the LLM cannot extract an unsafe number" rationale applied to
// only one of two consumers and left the operator tool reporting
// Compensation: "" for a posting Lever published a real range on.
//
// For an UNMAPPED interval (per-week-salary, per-day-wage, one-time) the
// number is now rendered with the interval token VERBATIM — e.g.
// "4000–6000 USD/per-week-salary" — so it reaches both consumers carrying its
// own disambiguation. The disease was an incomplete enum; hiding the data was
// not the cure. leverPostingToListing still refuses to assert a contract
// bucket in SalaryInterval (it stays empty in the structured listing), so the
// scorer never renders $X/year for a per-week posting; a model can reason
// about "per-week", it cannot reason about absence.
func leverSalaryString(p leverPosting) string {
	interval := normalizeSalaryInterval(p.SalaryRange.Interval)
	renderInterval := interval
	if renderInterval == "" {
		// Unmapped interval: render the raw token verbatim so the number is
		// not withheld from either consumer. Trimmed so a whitespace-only raw
		// interval falls through to the "" return below.
		renderInterval = strings.TrimSpace(p.SalaryRange.Interval)
	}
	if renderInterval == "" {
		// No interval information at all (empty raw + unmapped) — nothing
		// disambiguating to render alongside the number.
		return ""
	}
	if p.SalaryRange.Min <= 0 && p.SalaryRange.Max <= 0 {
		return ""
	}
	var minPtr, maxPtr *int
	if p.SalaryRange.Min > 0 {
		min := p.SalaryRange.Min
		minPtr = &min
	}
	if p.SalaryRange.Max > 0 {
		max := p.SalaryRange.Max
		maxPtr = &max
	}
	return formatSalary(minPtr, maxPtr, p.SalaryRange.Currency, renderInterval)
}

// leverPostingToListing builds a structured engine.JobListing from a parsed
// Lever posting, the board slug (company), the resolved job URL, and the
// resolved location string (categories.location, or allLocations joined — the
// caller already resolves this for the SearxngResult content). Populates every
// field the Lever API carries: text→Title, hostedUrl/applyUrl→URL,
// categories.commitment→JobType, workplaceType→Remote (normalized),
// salaryRange.{min,max,currency,interval}→SalaryMin/SalaryMax/SalaryCurrency/
// SalaryInterval (and the human-readable Salary string via formatSalary),
// createdAt→Posted (ISO via leverCreatedAtToISO), descriptionPlain→Description.
// Skills and Experience stay empty — Lever carries neither.
//
// Salary guards (BLOCKER C+D):
//   - Outer guard is Min > 0 || Max > 0 (NOT Min > 0 alone) so {min:0,
//     max:220000} is not dropped — the exact failure this PR exists to fix.
//   - Each pointer is set on its own > 0 test so {min:0,max:220000} yields
//     SalaryMax only, and {min:180000,max:180000} yields both (the old
//     Max > Min guard silently dropped single-figure comp).
//   - When interval is absent, ALL salary pointers stay nil — a per-hour
//     posting must not be scored as annual (BLOCKER D).
func leverPostingToListing(p leverPosting, slug, jobURL, loc string) engine.JobListing {
	desc := ""
	if p.DescriptionPlain != "" {
		desc = engine.TruncateRunes(p.DescriptionPlain, 600, "...")
	}
	l := engine.JobListing{
		Title:       p.Text,
		Company:     slug,
		URL:         jobURL,
		JobID:       p.ID,
		Source:      string(PlatformLever),
		Location:    loc,
		JobType:     p.Categories.Commitment,
		Remote:      normalizeRemote(p.WorkplaceType),
		Description: desc,
		Posted:      leverCreatedAtToISO(p.CreatedAt),
	}
	interval := normalizeSalaryInterval(p.SalaryRange.Interval)
	if interval == "" {
		// No interval → cannot determine annual vs hourly. Leave nil so
		// the scorer does not render $50–$80 USD/year for a per-hour posting.
		return l
	}
	if p.SalaryRange.Min > 0 || p.SalaryRange.Max > 0 {
		if p.SalaryRange.Min > 0 {
			min := p.SalaryRange.Min
			l.SalaryMin = &min
		}
		if p.SalaryRange.Max > 0 {
			max := p.SalaryRange.Max
			l.SalaryMax = &max
		}
		l.SalaryCurrency = p.SalaryRange.Currency
		l.SalaryInterval = interval
		l.Salary = formatSalary(l.SalaryMin, l.SalaryMax, p.SalaryRange.Currency, interval)
	}
	return l
}

// SearchLeverJobs discovers company slugs via multi-query union (P1) +
// runtime slug cache (P2), then hits the public JSON API for each slug.
// The former lever-only N=2 secondary block is replaced by the uniform
// unionDiscoverSlugs fan-out that covers the same query orderings.
// Returns only the SearxngResult slice; the structured JobListing collection is
// discarded here. Use SearchLeverJobsStructured to obtain it.
func SearchLeverJobs(ctx context.Context, query, location string, limit int) ([]engine.SearxngResult, error) {
	results, _, err := SearchLeverJobsStructured(ctx, query, location, limit)
	return results, err
}

// SearchLeverJobsStructured is the canonical implementation returning both the
// SearxngResult slice and a parallel []engine.JobListing keyed by the same URL.
// See SearchGreenhouseJobsStructured for the lockstep-append invariant.
func SearchLeverJobsStructured(ctx context.Context, query, location string, limit int) ([]engine.SearxngResult, []engine.JobListing, error) {
	engine.IncrLeverRequests()

	slugs := unionDiscoverSlugs(ctx, engine.DiscoveryPlatformLever, query, location, extractLeverSlugs)
	engine.IncrHuntDiscoveryURLs(engine.DiscoveryPlatformLever, len(slugs))
	if len(slugs) == 0 {
		slog.Debug("lever: no slugs found in discovery results")
		return nil, nil, nil
	}

	type fetchResult struct {
		slug     string
		postings []leverPosting
		err      error
	}
	ch := make(chan fetchResult, len(slugs))
	for _, slug := range slugs {
		go func(s string) {
			// BH-13: per-fetch timeout so one slow slug doesn't block the
			// entire platform's result collection until perPlatformTimeout.
			fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			postings, err := fetchLeverPostings(fetchCtx, s)
			ch <- fetchResult{s, postings, err}
		}(slug)
	}

	keywords := strings.Fields(strings.ToLower(query))
	var allResults []engine.SearxngResult
	var allListings []engine.JobListing
	for i := 0; i < len(slugs); i++ {
		r := <-ch
		if r.err != nil {
			slog.Warn("lever: fetch error", slog.String("slug", r.slug), slog.Any("error", r.err))
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
			if salStr := leverSalaryString(p); salStr != "" {
				content += " | **Salary:** " + salStr
			}
			if p.DescriptionPlain != "" {
				desc := engine.TruncateRunes(p.DescriptionPlain, 600, "...")
				content += "\n\n" + desc
			}
			sr := engine.SearxngResult{
				Title:   p.Text,
				Content: content,
				URL:     jobURL,
				Score:   0.9,
			}
			setPostedAtMeta(&sr, leverCreatedAtToISO(p.CreatedAt)) // lever createdAt is epoch ms
			allResults = append(allResults, sr)
			allListings = append(allListings, leverPostingToListing(p, r.slug, jobURL, loc))
			if len(allResults) >= limit {
				break
			}
		}
		if len(allResults) >= limit {
			break
		}
	}

	slog.Debug("lever: search complete", slog.Int("results", len(allResults)))
	return allResults, allListings, nil
}

// fetchLeverPostings fetches all job postings for a given company slug.
func fetchLeverPostings(ctx context.Context, slug string) (postings []leverPosting, err error) {
	if !leverBreaker.Allow() {
		engine.IncrATSBreakerOpen()
		return nil, fmt.Errorf("lever breaker open: %w", breaker.ErrOpen)
	}
	defer func() { leverBreaker.Record(err == nil) }()

	release, err := atsLimiter.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	apiURL := fmt.Sprintf(leverAPIBase, slug)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentBot)
	req.Header.Set("Accept", "application/json")

	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		return engine.Cfg.HTTPClient.Do(req) //nolint:gosec // ATS API URL from argument, intentional outbound request
	})
	if err != nil {
		engine.IncrATSFetchErrors(engine.DiscoveryPlatformLever, "transport")
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		if sc := GetSlugCache(); sc != nil {
			sc.Evict(engine.DiscoveryPlatformLever, slug, "board_404")
		}
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		engine.IncrATSFetchErrors(engine.DiscoveryPlatformLever, "status")
		return nil, fmt.Errorf("lever API status %d for %s", resp.StatusCode, slug)
	}

	if err := atsBoardDecode(resp.Body, &postings); err != nil {
		if isBodyTruncated(err) {
			engine.IncrATSFetchErrors(engine.DiscoveryPlatformLever, "truncated")
			return nil, fmt.Errorf("lever %s: %w", slug, ErrBodyTruncated)
		}
		engine.IncrATSFetchErrors(engine.DiscoveryPlatformLever, "parse")
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

// --- Ashby ---

//nolint:gochecknoglobals // var (not const) to allow test substitution
var ashbyBoardAPI = "https://api.ashbyhq.com/posting-api/job-board/%s?includeCompensation=true"

const ashbySiteSearch = "site:jobs.ashbyhq.com"

// ashbySlugRe extracts company slug from jobs.ashbyhq.com URLs.
var ashbySlugRe = regexp.MustCompile(`jobs\.ashbyhq\.com/([^/?#]+)`)

// ashbyJob is a single job from the Ashby public board API.
type ashbyJob struct {
	ID                 string `json:"id"`
	Title              string `json:"title"`
	Location           string `json:"location"`
	IsRemote           bool   `json:"isRemote"`
	WorkplaceType      string `json:"workplaceType"`
	SecondaryLocations []struct {
		Location string `json:"location"`
	} `json:"secondaryLocations"`
	JobURL           string `json:"jobUrl"`
	DescriptionPlain string `json:"descriptionPlain"`
	DescriptionHTML  string `json:"descriptionHtml"`
	Compensation     struct {
		CompensationTierSummary string `json:"compensationTierSummary"`
	} `json:"compensation"`
	Department  string `json:"department"`
	Team        string `json:"team"`
	PublishedAt string `json:"publishedAt"`
}

type ashbyResponse struct {
	Jobs []ashbyJob `json:"jobs"`
}

// ashbyJobToListing builds a structured engine.JobListing from a parsed Ashby
// job, the board slug (company), and the resolved job URL. Location is built
// via the existing buildAshbyLocation helper (reused — it folds isRemote and
// secondaryLocations into the location string). Remote is derived from
// workplaceType (normalized to the prompt vocabulary) with isRemote=true as
// the fallback when workplaceType is absent/unspecified. workplaceType wins
// because it is the MORE SPECIFIC field (remote/hybrid/onsite vs boolean) and
// the hunt_jobs.remote filter uses exact equality — storing "remote" for a
// hybrid job would make hunt_list remote=remote match it and remote=hybrid
// miss it. Salary carries compensationTierSummary verbatim (Ashby's
// structured comp string). Skills and Experience stay empty — Ashby carries
// neither here.
func ashbyJobToListing(j ashbyJob, slug, jobURL string) engine.JobListing {
	desc := ""
	if j.DescriptionPlain != "" {
		desc = engine.TruncateRunes(j.DescriptionPlain, 600, "...")
	}
	l := engine.JobListing{
		Title:       j.Title,
		Company:     slug,
		URL:         jobURL,
		JobID:       j.ID,
		Source:      string(PlatformAshby),
		Location:    buildAshbyLocation(j),
		Salary:      j.Compensation.CompensationTierSummary,
		Description: desc,
		Posted:      j.PublishedAt,
	}
	if r := normalizeRemote(j.WorkplaceType); r != "" {
		l.Remote = r
	} else if j.IsRemote {
		l.Remote = "remote" //nolint:goconst // semantic: JobListing.Remote field value, distinct from algoraRemoteRow (algora Tier-2 row-walk key)
	}
	return l
}

// SearchAshbyJobs discovers company slugs via multi-query union (P1) +
// runtime slug cache (P2), then hits the public JSON API for each slug.
// Returns only the SearxngResult slice; the structured JobListing collection is
// discarded here. Use SearchAshbyJobsStructured to obtain it.
func SearchAshbyJobs(ctx context.Context, query, location string, limit int) ([]engine.SearxngResult, error) {
	results, _, err := SearchAshbyJobsStructured(ctx, query, location, limit)
	return results, err
}

// SearchAshbyJobsStructured is the canonical implementation returning both the
// SearxngResult slice and a parallel []engine.JobListing keyed by the same URL.
// See SearchGreenhouseJobsStructured for the lockstep-append invariant.
func SearchAshbyJobsStructured(ctx context.Context, query, location string, limit int) ([]engine.SearxngResult, []engine.JobListing, error) {
	engine.IncrAshbyRequests()

	slugs := unionDiscoverSlugs(ctx, engine.DiscoveryPlatformAshby, query, location, extractAshbySlugs)
	engine.IncrHuntDiscoveryURLs(engine.DiscoveryPlatformAshby, len(slugs))
	if len(slugs) == 0 {
		slog.Debug("ashby: no slugs found in discovery results")
		return nil, nil, nil
	}

	type fetchResult struct {
		slug string
		jobs []ashbyJob
		err  error
	}
	ch := make(chan fetchResult, len(slugs))
	for _, slug := range slugs {
		go func(s string) {
			// BH-13: per-fetch timeout so one slow slug doesn't block the
			// entire platform's result collection until perPlatformTimeout.
			fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			fetched, ferr := fetchAshbyJobs(fetchCtx, s)
			ch <- fetchResult{s, fetched, ferr}
		}(slug)
	}

	keywords := strings.Fields(strings.ToLower(query))
	var allResults []engine.SearxngResult
	var allListings []engine.JobListing
	for i := 0; i < len(slugs); i++ {
		r := <-ch
		if r.err != nil {
			slog.Warn("ashby: fetch error", slog.String("slug", r.slug), slog.Any("error", r.err))
			continue
		}
		for _, j := range r.jobs {
			if !matchesKeywords(j.Title+" "+j.Location, keywords) {
				continue
			}
			jobURL := j.JobURL
			if jobURL == "" {
				jobURL = fmt.Sprintf("https://jobs.ashbyhq.com/%s/%s", r.slug, j.ID)
			}
			loc := buildAshbyLocation(j)
			content := fmt.Sprintf("**Source:** Ashby | **Company:** %s | **Location:** %s", r.slug, loc)
			if j.WorkplaceType != "" {
				content += " | **Type:** " + j.WorkplaceType
			}
			if j.Department != "" {
				content += " | **Dept:** " + j.Department
			}
			if j.Compensation.CompensationTierSummary != "" {
				content += " | **Comp:** " + j.Compensation.CompensationTierSummary
			}
			if j.PublishedAt != "" && len(j.PublishedAt) >= 10 {
				content += " | **Published:** " + j.PublishedAt[:10]
			}
			if j.DescriptionPlain != "" {
				desc := engine.TruncateRunes(j.DescriptionPlain, 600, "...")
				content += "\n\n" + desc
			}
			sr := engine.SearxngResult{
				Title:   j.Title,
				Content: content,
				URL:     jobURL,
				Score:   0.9,
			}
			setPostedAtMeta(&sr, j.PublishedAt) // ashby publishedAt is RFC3339 ISO
			allResults = append(allResults, sr)
			allListings = append(allListings, ashbyJobToListing(j, r.slug, jobURL))
			if len(allResults) >= limit {
				break
			}
		}
		if len(allResults) >= limit {
			break
		}
	}

	slog.Debug("ashby: search complete", slog.Int("results", len(allResults)))
	return allResults, allListings, nil
}

// fetchAshbyJobs fetches all jobs for a given company slug.
//
//nolint:dupl // structurally similar to fetchGreenhouseJobs — different types, API URL pattern, and body limit (5MB vs 2MB)
func fetchAshbyJobs(ctx context.Context, slug string) (jobs []ashbyJob, err error) {
	if !ashbyBreaker.Allow() {
		engine.IncrATSBreakerOpen()
		return nil, fmt.Errorf("ashby breaker open: %w", breaker.ErrOpen)
	}
	defer func() { ashbyBreaker.Record(err == nil) }()

	release, err := atsLimiter.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	apiURL := fmt.Sprintf(ashbyBoardAPI, slug)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentBot)
	req.Header.Set("Accept", "application/json")

	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		return engine.Cfg.HTTPClient.Do(req) //nolint:gosec // ATS API URL from argument, intentional outbound request
	})
	if err != nil {
		engine.IncrATSFetchErrors(engine.DiscoveryPlatformAshby, "transport")
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		if sc := GetSlugCache(); sc != nil {
			sc.Evict(engine.DiscoveryPlatformAshby, slug, "board_404")
		}
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		engine.IncrATSFetchErrors(engine.DiscoveryPlatformAshby, "status")
		return nil, fmt.Errorf("ashby API status %d for %s", resp.StatusCode, slug)
	}

	// Stream-decode: no full-body ReadAll; DoS ceiling via atsBoardMaxBytes LimitReader.
	var ar ashbyResponse
	if err := atsBoardDecode(resp.Body, &ar); err != nil {
		if isBodyTruncated(err) {
			engine.IncrATSFetchErrors(engine.DiscoveryPlatformAshby, "truncated")
			return nil, fmt.Errorf("ashby %s: %w", slug, ErrBodyTruncated)
		}
		engine.IncrATSFetchErrors(engine.DiscoveryPlatformAshby, "parse")
		return nil, fmt.Errorf("ashby parse: %w", err)
	}
	return ar.Jobs, nil
}

// extractAshbySlugs extracts unique company slugs from SearXNG result URLs.
func extractAshbySlugs(results []engine.SearxngResult) []string {
	seen := make(map[string]bool)
	var slugs []string
	for _, r := range results {
		if m := ashbySlugRe.FindStringSubmatch(r.URL); m != nil {
			slug := strings.ToLower(m[1])
			if slug != "" && !seen[slug] {
				seen[slug] = true
				slugs = append(slugs, slug)
			}
		}
	}
	return slugs
}

// buildAshbyLocation constructs a location string from an ashbyJob,
// combining primary location, remote status, and secondary locations.
func buildAshbyLocation(j ashbyJob) string {
	loc := j.Location
	if j.IsRemote {
		if loc == "" {
			loc = locationRemote
		} else {
			loc += " | Remote"
		}
	}
	if len(j.SecondaryLocations) > 0 {
		var sec []string
		for _, s := range j.SecondaryLocations {
			if s.Location != "" {
				sec = append(sec, s.Location)
			}
		}
		if len(sec) > 0 {
			loc += " (+" + strings.Join(sec, ", ") + ")"
		}
	}
	return loc
}

// NormalizeURL canonicalizes a job URL for use as a map join key. It lowercases
// the host, strips query and fragment, and removes any trailing slash. This
// makes the producer side (code-built SearxngResult.URL) and the lookup side
// (LLM-emitted jobOut.Jobs[i].URL, which can carry trailing slashes, query
// params, or mixed casing) match — without it, structuredByURL is an exact-
// string map and a single trailing slash yields zero hits (the HIGH finding:
// "zero hits for structured data").
//
// Exported so tool_job_search.go can build the producer-side map with the same
// canonicalization the lookup side (StructuredMatcher) uses.
func NormalizeURL(u string) string {
	parsed, err := url.Parse(strings.TrimSpace(u))
	if err != nil || parsed.Host == "" {
		// Not a valid absolute URL — fall back to a best-effort trim so the
		// caller's ExtractJobID fallback still has something to work with.
		return strings.TrimRight(strings.TrimSpace(u), "/")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed.String()
}

// StructuredMatcher resolves the join of an LLM-extracted JobListing to its
// structured counterpart. It is the SINGLE place the join lives: the
// normalized-URL index, the JobID fallback index, and the source-equality
// guard that refuses a cross-provider JobID collision.
// buildHealthySelection in tool_job_search.go (structured-is-spine, LLM fills
// gaps) resolves its match through this type so the join logic is not
// duplicated.
//
// Join key: the primary lookup is NormalizeURL(llm.URL) against an index built
// from NormalizeURL(s.URL) for each structured listing — a trailing slash,
// query param, or host casing variation no longer yields zero hits. When the
// normalized URL misses, the lookup falls back to llm.JobID matched against a
// byJobID index (built from each structured listing's JobID). This catches the
// case where the LLM emits a different URL for the same job (e.g. the apply URL
// vs the hosted URL on Lever) but extracted the JobID from the posting body.
type StructuredMatcher struct {
	byNormURL map[string]engine.JobListing
	byJobID   map[string]engine.JobListing
}

// NewStructuredMatcher builds the normalized-URL and JobID indices from
// structuredByURL. First-write-wins on duplicate keys.
func NewStructuredMatcher(structuredByURL map[string]engine.JobListing) *StructuredMatcher {
	m := &StructuredMatcher{
		byNormURL: make(map[string]engine.JobListing, len(structuredByURL)),
		byJobID:   make(map[string]engine.JobListing, len(structuredByURL)),
	}
	for _, s := range structuredByURL {
		k := NormalizeURL(s.URL)
		if _, exists := m.byNormURL[k]; !exists {
			m.byNormURL[k] = s
		}
		if s.JobID != "" {
			if _, exists := m.byJobID[s.JobID]; !exists {
				m.byJobID[s.JobID] = s
			}
		}
	}
	return m
}

// Match returns the structured listing joined to llm, or ok=false when no
// structured counterpart exists. The normalized-URL lookup is tried first; on
// miss the JobID fallback runs under the source-equality guard.
//
// Fallback: match by llm.JobID (the LLM may have extracted it from the posting
// body even when the URL is wrong). Requires the LLM record's Source to equal
// the candidate's Source — a cross-provider JobID collision must not rewrite
// the wrong record. ExtractJobID is NOT used here: it matches only LinkedIn
// URLs (/jobs/view/…), so it can never produce a Lever/Greenhouse/Ashby id,
// and the one case it does return (a LinkedIn id) can collide with a
// Greenhouse int64 id in byJobID.
//
// When the LLM record omits Source (json:"source,omitempty", nothing enforces
// it, and precedence runs BEFORE the extractSourceForQuality backfill in
// tool_job_search.go), resolve it from the URL via extractSourceFromURL and
// require equality with the candidate's Source. If the URL is also
// unresolvable, refuse the JobID fallback — otherwise a LinkedIn record (id
// 4001234, no Source) would match a Greenhouse candidate with the same int64
// id and be silently relabelled.
//
// Observability: every call emits gojob_structured_precedence_total{source,
// outcome} so the join hit rate per arm is visible in Prometheus and a
// URL-join-key regression (no_match ratio → 1.0) or a JobID-fallback
// regression (jobid_fallback rate → 0) is detectable. outcome=url_match for
// a normalized-URL hit, outcome=jobid_fallback for a JobID-fallback hit (the
// arm that was silently missing — its rate is the regression signal),
// outcome=no_match for a miss. source is the matched listing's Source for
// matches, or extractSourceFromURL(llm.URL) for no_match, falling back to
// "none" when the URL is unresolvable so a join regression cannot hide as
// "no ATS jobs in this search". Emission lives HERE (not in the caller) because
// Match is the single place the join outcome is known: the caller
// (buildHealthySelection) receives only (listing, ok) and would have to
// re-derive which arm fired to attribute the counter — re-running the lookup
// in the caller would duplicate the join logic. The caller always acts on a
// match (emits structured) and on a miss (emits LLM unchanged), so there is no
// conditional where a match is silently dropped — every Match call produces
// exactly one counter increment, and that increment IS the signal.
func (m *StructuredMatcher) Match(llm engine.JobListing) (engine.JobListing, bool) {
	s, ok := m.byNormURL[NormalizeURL(llm.URL)]
	if ok {
		engine.IncrStructuredPrecedence(s.Source, "url_match")
		return s, true
	}
	if llm.JobID != "" {
		cand, candOk := m.byJobID[llm.JobID]
		if candOk {
			llmSrc := llm.Source
			if llmSrc == "" {
				llmSrc = extractSourceFromURL(llm.URL)
			}
			if llmSrc != "" && cand.Source == llmSrc {
				engine.IncrStructuredPrecedence(cand.Source, "jobid_fallback")
				return cand, true
			}
		}
	}
	src := extractSourceFromURL(llm.URL)
	if src == "" {
		src = "none"
	}
	engine.IncrStructuredPrecedence(src, "no_match")
	return engine.JobListing{}, false
}

// FillStructuredFromLLM fills the EMPTY fields of a structured listing from
// the matching LLM listing. The structured listing is the spine (authoritative,
// machine-extracted from the ATS API); the LLM contributes ONLY where the
// structured source is silent.
//
// Salary group coherence: structured free-text (Salary) is never paired with
// LLM-guessed numerics (SalaryMin/Max). LLM numerics are NOT filled into a
// structured listing that already carries its own free-text salary, because the
// LLM's numeric guess could disagree with the authoritative structured text
// (the Ashby case: compensationTierSummary is the precise string, LLM numerics
// are a guess). LLM free-text Salary is filled whenever structured is empty
// (structured numerics + LLM free-text is allowed — the structured numerics are
// authoritative and the LLM free-text is a display string).
//
// Description is intentionally NOT filled: the structured source's full
// description is authoritative where present, and where it is absent the LLM's
// summary is not grafted on — the LLM-only listings carry their own description.
func FillStructuredFromLLM(s *engine.JobListing, llm engine.JobListing) {
	if s.Title == "" {
		s.Title = llm.Title
	}
	if s.Company == "" {
		s.Company = llm.Company
	}
	if s.URL == "" {
		s.URL = llm.URL
	}
	if s.JobID == "" {
		s.JobID = llm.JobID
	}
	if s.Source == "" {
		s.Source = llm.Source
	}
	if s.Location == "" {
		s.Location = llm.Location
	}
	// Salary group, field by field (strictly additive). A structured value is
	// kept wherever it is non-empty; the LLM fills only the gaps. The coherence
	// guard: LLM numerics (SalaryMin/Max) are filled ONLY when the STRUCTURED
	// listing carried no free-text Salary — otherwise the LLM's guessed numerics
	// could disagree with the authoritative structured free-text (the Ashby
	// case: compensationTierSummary is the precise string, LLM numerics are a
	// guess). LLM free-text Salary is filled whenever structured is empty
	// (structured numerics + LLM free-text is allowed).
	//
	// structuredHadSalary is captured BEFORE the Salary fill so the numeric
	// guard reads the ORIGINAL structured state, not the post-fill s.Salary the
	// previous line just mutated. The old guard (s.Salary == "" checked after
	// the Salary fill) blocked LLM numerics for every job whose salary came
	// from the LLM — the common Greenhouse path (Greenhouse publishes no comp
	// field, so the LLM's free-text + numerics are the only salary and are
	// coherent: same source, same record). A guard that depends on statement
	// order is the defect; capturing the original state makes the guard
	// order-independent.
	structuredHadSalary := s.Salary != ""
	if s.Salary == "" && llm.Salary != "" {
		s.Salary = llm.Salary
	}
	if s.SalaryMin == nil && !structuredHadSalary && llm.SalaryMin != nil {
		s.SalaryMin = llm.SalaryMin
	}
	if s.SalaryMax == nil && !structuredHadSalary && llm.SalaryMax != nil {
		s.SalaryMax = llm.SalaryMax
	}
	if s.SalaryCurrency == "" && llm.SalaryCurrency != "" {
		s.SalaryCurrency = llm.SalaryCurrency
	}
	if s.SalaryInterval == "" && llm.SalaryInterval != "" {
		s.SalaryInterval = llm.SalaryInterval
	}
	if s.JobType == "" {
		s.JobType = llm.JobType
	}
	if s.Remote == "" {
		s.Remote = llm.Remote
	}
	if s.Experience == "" {
		s.Experience = llm.Experience
	}
	if len(s.Skills) == 0 {
		s.Skills = llm.Skills
	}
	// Description intentionally NOT filled — see doc comment.
	if s.Posted == "" {
		s.Posted = llm.Posted
	}
}

// extractSourceFromURL infers the ATS provider from a URL for the JobID-fallback
// Source resolution in StructuredMatcher.Match. Returns "" for non-ATS URLs;
// the caller refuses the fallback when the resolved Source is empty so a
// cross-provider JobID collision cannot rewrite the wrong record. Delegates to
// SourceFromURL (the single shared URL→source implementation in hunt_map.go)
// and keeps only the ATS arms.
func extractSourceFromURL(jobURL string) string {
	s := SourceFromURL(jobURL)
	switch s {
	case string(PlatformGreenhouse), string(PlatformLever), string(PlatformAshby):
		return s
	default:
		return ""
	}
}

// --- Public API: URL parsing + direct board fetch ---

// ATSPlatform identifies which ATS hosts a given URL.
type ATSPlatform string

const (
	PlatformGreenhouse ATSPlatform = "greenhouse"
	PlatformAshby      ATSPlatform = "ashby"
	PlatformLever      ATSPlatform = "lever"
	PlatformUnknown    ATSPlatform = "unknown"
)

// greenhouseJobRe matches Greenhouse job URLs (boards.greenhouse.io or job-boards.greenhouse.io).
var greenhouseJobRe = regexp.MustCompile(`(?:boards|job-boards)\.greenhouse\.io/([^/?#]+)/jobs/(\d+)`)

// leverJobRe matches Lever job URLs: jobs.lever.co/<org>/<uuid>.
var leverJobRe = regexp.MustCompile(`jobs\.lever\.co/([^/?#]+)/([^/?#]+)`)

// ashbyJobRe matches Ashby job URLs: jobs.ashbyhq.com/<org>/<uuid>.
var ashbyJobRe = regexp.MustCompile(`jobs\.ashbyhq\.com/([^/?#]+)/([^/?#]+)`)

// ATSURLInfo decomposes a known ATS URL into structured fields.
type ATSURLInfo struct {
	Platform     ATSPlatform `json:"platform"`
	Org          string      `json:"org"`
	JobID        string      `json:"job_id,omitempty"`
	APIURL       string      `json:"api_url,omitempty"`
	CanonicalURL string      `json:"canonical_url,omitempty"`
}

// ParseATSURL identifies platform + org + job_id from any supported ATS URL.
// Returns platform=PlatformUnknown (not an error) when no pattern matches.
func ParseATSURL(rawURL string) (*ATSURLInfo, error) {
	if m := greenhouseJobRe.FindStringSubmatch(rawURL); m != nil {
		org := strings.ToLower(m[1])
		jobID := m[2]
		return &ATSURLInfo{
			Platform:     PlatformGreenhouse,
			Org:          org,
			JobID:        jobID,
			APIURL:       fmt.Sprintf(greenhouseBoardsAPI, org),
			CanonicalURL: fmt.Sprintf("https://boards.greenhouse.io/%s/jobs/%s", org, jobID),
		}, nil
	}
	if m := ashbyJobRe.FindStringSubmatch(rawURL); m != nil {
		org := strings.ToLower(m[1])
		jobID := m[2]
		return &ATSURLInfo{
			Platform:     PlatformAshby,
			Org:          org,
			JobID:        jobID,
			APIURL:       fmt.Sprintf(ashbyBoardAPI, org),
			CanonicalURL: fmt.Sprintf("https://jobs.ashbyhq.com/%s/%s", org, jobID),
		}, nil
	}
	if m := leverJobRe.FindStringSubmatch(rawURL); m != nil {
		org := strings.ToLower(m[1])
		jobID := m[2]
		return &ATSURLInfo{
			Platform:     PlatformLever,
			Org:          org,
			JobID:        jobID,
			APIURL:       fmt.Sprintf(leverAPIBase, org),
			CanonicalURL: fmt.Sprintf("https://jobs.lever.co/%s/%s", org, jobID),
		}, nil
	}
	// Board-level URLs (no job_id) — still detect platform + org.
	if m := greenhouseSlugRe.FindStringSubmatch(rawURL); m != nil {
		org := strings.ToLower(m[1])
		return &ATSURLInfo{
			Platform:     PlatformGreenhouse,
			Org:          org,
			APIURL:       fmt.Sprintf(greenhouseBoardsAPI, org),
			CanonicalURL: "https://boards.greenhouse.io/" + org,
		}, nil
	}
	if m := ashbySlugRe.FindStringSubmatch(rawURL); m != nil {
		org := strings.ToLower(m[1])
		return &ATSURLInfo{
			Platform:     PlatformAshby,
			Org:          org,
			APIURL:       fmt.Sprintf(ashbyBoardAPI, org),
			CanonicalURL: "https://jobs.ashbyhq.com/" + org,
		}, nil
	}
	if m := leverSlugRe.FindStringSubmatch(rawURL); m != nil {
		org := strings.ToLower(m[1])
		return &ATSURLInfo{
			Platform:     PlatformLever,
			Org:          org,
			APIURL:       fmt.Sprintf(leverAPIBase, org),
			CanonicalURL: "https://jobs.lever.co/" + org,
		}, nil
	}
	return &ATSURLInfo{Platform: PlatformUnknown}, nil
}

// ATSJob is a normalized job representation across Greenhouse, Ashby, and Lever.
type ATSJob struct {
	ID           string      `json:"id"`
	Title        string      `json:"title"`
	Company      string      `json:"company"`
	Location     string      `json:"location"`
	URL          string      `json:"url"`
	Compensation string      `json:"compensation,omitempty"`
	Department   string      `json:"department,omitempty"`
	Team         string      `json:"team,omitempty"`
	Description  string      `json:"description,omitempty"` // plain text, truncated to 2000 chars
	PublishedAt  string      `json:"published_at,omitempty"`
	Platform     ATSPlatform `json:"platform"`
}

// FetchATSBoardInput controls the direct board fetch.
type FetchATSBoardInput struct {
	Org                string `json:"org"`
	Platform           string `json:"platform"`                      // "greenhouse"|"ashby"|"lever"
	Limit              int    `json:"limit,omitempty"`               // default 100, max 500
	Query              string `json:"query,omitempty"`               // optional case-insensitive title substring
	IncludeDescription bool   `json:"include_description,omitempty"` // default false
}

// FetchATSBoardResult is the unified board fetch response.
type FetchATSBoardResult struct {
	Jobs     []ATSJob    `json:"jobs"`
	Total    int         `json:"total"`
	Org      string      `json:"org"`
	Platform ATSPlatform `json:"platform"`
}

const (
	fetchATSBoardDefaultLimit = 100
	fetchATSBoardMaxLimit     = 500
	atsDescriptionMaxRunes    = 2000
)

// FetchATSBoard hits the platform's public board JSON directly using the known org slug.
// No SearXNG slug discovery — caller already knows the slug.
func FetchATSBoard(ctx context.Context, input FetchATSBoardInput) (*FetchATSBoardResult, error) {
	limit := input.Limit
	if limit <= 0 {
		limit = fetchATSBoardDefaultLimit
	}
	if limit > fetchATSBoardMaxLimit {
		limit = fetchATSBoardMaxLimit
	}
	queryLower := strings.ToLower(input.Query)

	platform := ATSPlatform(strings.ToLower(input.Platform))
	var jobs []ATSJob

	switch platform {
	case PlatformGreenhouse:
		raw, err := fetchGreenhouseJobs(ctx, input.Org)
		if err != nil {
			return nil, fmt.Errorf("greenhouse fetch: %w", err)
		}
		for _, j := range raw {
			if queryLower != "" && !strings.Contains(strings.ToLower(j.Title), queryLower) {
				continue
			}
			jobURL := j.AbsoluteURL
			if jobURL == "" {
				jobURL = fmt.Sprintf("https://boards.greenhouse.io/%s/jobs/%d", input.Org, j.ID)
			}
			dept := ""
			if len(j.Departments) > 0 {
				dept = j.Departments[0].Name
			}
			desc := ""
			if input.IncludeDescription && j.Content != "" {
				desc = engine.TruncateRunes(engine.CleanHTML(j.Content), atsDescriptionMaxRunes, "...")
			}
			jobs = append(jobs, ATSJob{
				ID:          strconv.FormatInt(j.ID, 10),
				Title:       j.Title,
				Company:     input.Org,
				Location:    j.Location.Name,
				URL:         jobURL,
				Department:  dept,
				Description: desc,
				PublishedAt: j.UpdatedAt,
				Platform:    PlatformGreenhouse,
			})
			if len(jobs) >= limit {
				break
			}
		}

	case PlatformAshby:
		raw, err := fetchAshbyJobs(ctx, input.Org)
		if err != nil {
			return nil, fmt.Errorf("ashby fetch: %w", err)
		}
		for _, j := range raw {
			if queryLower != "" && !strings.Contains(strings.ToLower(j.Title), queryLower) {
				continue
			}
			jobURL := j.JobURL
			if jobURL == "" {
				jobURL = fmt.Sprintf("https://jobs.ashbyhq.com/%s/%s", input.Org, j.ID)
			}
			desc := ""
			if input.IncludeDescription && j.DescriptionPlain != "" {
				desc = engine.TruncateRunes(j.DescriptionPlain, atsDescriptionMaxRunes, "...")
			}
			jobs = append(jobs, ATSJob{
				ID:           j.ID,
				Title:        j.Title,
				Company:      input.Org,
				Location:     buildAshbyLocation(j),
				URL:          jobURL,
				Compensation: j.Compensation.CompensationTierSummary,
				Department:   j.Department,
				Team:         j.Team,
				Description:  desc,
				PublishedAt:  j.PublishedAt,
				Platform:     PlatformAshby,
			})
			if len(jobs) >= limit {
				break
			}
		}

	case PlatformLever:
		raw, err := fetchLeverPostings(ctx, input.Org)
		if err != nil {
			return nil, fmt.Errorf("lever fetch: %w", err)
		}
		for _, p := range raw {
			if queryLower != "" && !strings.Contains(strings.ToLower(p.Text), queryLower) {
				continue
			}
			jobURL := p.HostedURL
			if jobURL == "" {
				jobURL = fmt.Sprintf("https://jobs.lever.co/%s/%s", input.Org, p.ID)
			}
			loc := p.Categories.Location
			if loc == "" && len(p.Categories.AllLocations) > 0 {
				loc = strings.Join(p.Categories.AllLocations, ", ")
			}
			comp := leverSalaryString(p)
			desc := ""
			if input.IncludeDescription && p.DescriptionPlain != "" {
				desc = engine.TruncateRunes(p.DescriptionPlain, atsDescriptionMaxRunes, "...")
			}
			jobs = append(jobs, ATSJob{
				ID:           p.ID,
				Title:        p.Text,
				Company:      input.Org,
				Location:     loc,
				URL:          jobURL,
				Compensation: comp,
				Department:   p.Categories.Department,
				Team:         p.Categories.Team,
				Description:  desc,
				Platform:     PlatformLever,
			})
			if len(jobs) >= limit {
				break
			}
		}

	default:
		return nil, fmt.Errorf("unsupported platform %q (supported: greenhouse, ashby, lever)", input.Platform)
	}

	if jobs == nil {
		jobs = []ATSJob{}
	}
	return &FetchATSBoardResult{
		Jobs:     jobs,
		Total:    len(jobs),
		Org:      input.Org,
		Platform: platform,
	}, nil
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

// extractATSCompanyName is a helper used by the tool layer to pull company name from ATS URLs.
func extractATSCompanyName(rawURL string) string {
	if m := greenhouseSlugRe.FindStringSubmatch(rawURL); m != nil {
		return m[1]
	}
	if m := leverSlugRe.FindStringSubmatch(rawURL); m != nil {
		return m[1]
	}
	if m := ashbySlugRe.FindStringSubmatch(rawURL); m != nil {
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
