package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/anatolykoptev/go-kit/breaker"
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

func getATSMaxConcurrent() int {
	if v := os.Getenv("GO_JOB_ATS_MAX_CONCURRENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 3
}

// Per-provider circuit breakers. After FailThreshold consecutive failures the
// breaker opens for OpenDuration, blocking further attempts. After cooldown it
// half-opens for one probe; if that succeeds the breaker resets to closed.
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
		Name:          "greenhouse",
		FailThreshold: 3,
		OpenDuration:  30 * time.Second,
	})
	leverBreaker = breaker.New(breaker.Options{
		Name:          "lever",
		FailThreshold: 3,
		OpenDuration:  30 * time.Second,
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
//   - transport / decode error → fall through to local scrapers (the degraded floor).
//
// When ATSDiscoverer is nil, or after a go-search transport error, the local path
// (go-engine DIRECT + additive SearXNG) runs as the degraded floor.  A degraded
// run is observable via gojob_hunt_discovery_source_total{source="local-fallback"}
// rising; outcome=empty on ATS sources is the expected downstream signal rather
// than a permanent-floor guarantee.
func discoverJobURLs(ctx context.Context, query string) []engine.SearxngResult {
	if ATSDiscoverer != nil {
		results, err := ATSDiscoverer.DiscoverBoardURLs(ctx, query)
		if err != nil {
			// go-search unavailable — fall through to local scrapers.
			slog.Warn("discover: go-search unavailable, falling back to local",
				slog.String("query", query),
				slog.Any("error", err),
			)
			engine.IncrHuntDiscoverySource("local-fallback")
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
	// SearXNG is additive: when unconfigured it returns nil,nil — harmless.
	searx, err := engine.SearchSearXNG(ctx, query, "all", "", engine.DefaultSearchEngine)
	if err != nil {
		slog.Debug("discover: SearXNG error (additive source)", slog.Any("error", err))
	}
	return deduplicateByURL(append(direct, searx...))
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

// --- Greenhouse ---

const greenhouseBoardsAPI = "https://boards-api.greenhouse.io/v1/boards/%s/jobs"
const greenhouseSiteSearch = "site:boards.greenhouse.io"

// maxATSSlugsPerSearch caps how many company slugs we fan-out to per ATS source per query.
const maxATSSlugsPerSearch = 5

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

	// Extract unique company slugs from discovery URLs (DIRECT primary + SearXNG additive).
	slugs := extractGreenhouseSlugs(discoverJobURLs(ctx, searxQuery))
	if len(slugs) == 0 {
		slog.Debug("greenhouse: no slugs found in discovery results")
		return nil, nil
	}
	if len(slugs) > maxATSSlugsPerSearch {
		slugs = slugs[:maxATSSlugsPerSearch]
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
//
//nolint:dupl // structurally similar to fetchAshbyJobs/fetchLeverPostings — different types, URLs, body limits
func fetchGreenhouseJobs(ctx context.Context, slug string) ([]greenhouseJob, error) {
	if !greenhouseBreaker.Allow() {
		return nil, fmt.Errorf("greenhouse breaker open: %w", breaker.ErrOpen)
	}

	release, err := atsLimiter.Acquire(ctx)
	if err != nil {
		greenhouseBreaker.Record(false)
		return nil, err
	}
	defer release()

	apiURL := fmt.Sprintf(greenhouseBoardsAPI, slug)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		greenhouseBreaker.Record(false)
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentBot)
	req.Header.Set("Accept", "application/json")

	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		return engine.Cfg.HTTPClient.Do(req) //nolint:gosec // ATS API URL from argument, intentional outbound request
	})
	if err != nil {
		greenhouseBreaker.Record(false)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		greenhouseBreaker.Record(true) // 404 = valid response, not a failure
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		greenhouseBreaker.Record(false)
		return nil, fmt.Errorf("greenhouse API status %d for %s", resp.StatusCode, slug)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		greenhouseBreaker.Record(false)
		return nil, err
	}

	var gr greenhouseResponse
	if err := json.Unmarshal(body, &gr); err != nil {
		greenhouseBreaker.Record(false)
		return nil, fmt.Errorf("greenhouse parse: %w", err)
	}
	greenhouseBreaker.Record(true)
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

	slugs := extractLeverSlugs(discoverJobURLs(ctx, searxQuery))

	// Secondary discovery: put the site scope first so search engines apply
	// "site:jobs.lever.co" filtering at the front of the query — some engines
	// yield more on-domain results with the site: operator leading rather than
	// trailing. Only tried when the primary pass returned nothing, to avoid
	// duplicate fetches on the fast path.
	if len(slugs) == 0 {
		var altQuery string
		if location != "" {
			altQuery = leverSiteSearch + " " + query + " " + location
		} else {
			altQuery = leverSiteSearch + " " + query
		}
		slugs = extractLeverSlugs(discoverJobURLs(ctx, altQuery))
		if len(slugs) > 0 {
			slog.Debug("lever: primary discovery empty; secondary (site-scope-first) yielded slugs",
				slog.Int("slugs", len(slugs)))
		}
	}

	if len(slugs) == 0 {
		slog.Debug("lever: no slugs found in discovery results (both queries)")
		return nil, nil
	}
	if len(slugs) > maxATSSlugsPerSearch {
		slugs = slugs[:maxATSSlugsPerSearch]
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
	if !leverBreaker.Allow() {
		return nil, fmt.Errorf("lever breaker open: %w", breaker.ErrOpen)
	}

	release, err := atsLimiter.Acquire(ctx)
	if err != nil {
		leverBreaker.Record(false)
		return nil, err
	}
	defer release()

	apiURL := fmt.Sprintf(leverAPIBase, slug)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		leverBreaker.Record(false)
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentBot)
	req.Header.Set("Accept", "application/json")

	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		return engine.Cfg.HTTPClient.Do(req) //nolint:gosec // ATS API URL from argument, intentional outbound request
	})
	if err != nil {
		leverBreaker.Record(false)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		leverBreaker.Record(true) // 404 = valid response, not a failure
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		leverBreaker.Record(false)
		return nil, fmt.Errorf("lever API status %d for %s", resp.StatusCode, slug)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		leverBreaker.Record(false)
		return nil, err
	}

	var postings []leverPosting
	if err := json.Unmarshal(body, &postings); err != nil {
		leverBreaker.Record(false)
		return nil, fmt.Errorf("lever parse: %w", err)
	}
	leverBreaker.Record(true)
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
	ID            string `json:"id"`
	Title         string `json:"title"`
	Location      string `json:"location"`
	IsRemote      bool   `json:"isRemote"`
	WorkplaceType string `json:"workplaceType"`
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

// SearchAshbyJobs discovers company slugs via SearXNG then hits the public JSON API.
// Mirrors SearchGreenhouseJobs structure for consistency.
func SearchAshbyJobs(ctx context.Context, query, location string, limit int) ([]engine.SearxngResult, error) {
	engine.IncrAshbyRequests()

	searxQuery := query + " " + ashbySiteSearch
	if location != "" {
		searxQuery = query + " " + location + " " + ashbySiteSearch
	}

	slugs := extractAshbySlugs(discoverJobURLs(ctx, searxQuery))
	if len(slugs) == 0 {
		slog.Debug("ashby: no slugs found in discovery results")
		return nil, nil
	}
	if len(slugs) > maxATSSlugsPerSearch {
		slugs = slugs[:maxATSSlugsPerSearch]
	}

	type fetchResult struct {
		slug string
		jobs []ashbyJob
		err  error
	}
	ch := make(chan fetchResult, len(slugs))
	for _, slug := range slugs {
		go func(s string) {
			fetched, ferr := fetchAshbyJobs(ctx, s)
			ch <- fetchResult{s, fetched, ferr}
		}(slug)
	}

	keywords := strings.Fields(strings.ToLower(query))
	var allResults []engine.SearxngResult
	for i := 0; i < len(slugs); i++ {
		r := <-ch
		if r.err != nil {
			slog.Debug("ashby: fetch error", slog.String("slug", r.slug), slog.Any("error", r.err))
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
			allResults = append(allResults, engine.SearxngResult{
				Title:   j.Title,
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

	slog.Debug("ashby: search complete", slog.Int("results", len(allResults)))
	return allResults, nil
}

// fetchAshbyJobs fetches all jobs for a given company slug.
//
//nolint:dupl // structurally similar to fetchGreenhouseJobs — different types, API URL pattern, and body limit (5MB vs 2MB)
func fetchAshbyJobs(ctx context.Context, slug string) ([]ashbyJob, error) {
	if !ashbyBreaker.Allow() {
		return nil, fmt.Errorf("ashby breaker open: %w", breaker.ErrOpen)
	}

	release, err := atsLimiter.Acquire(ctx)
	if err != nil {
		ashbyBreaker.Record(false)
		return nil, err
	}
	defer release()

	apiURL := fmt.Sprintf(ashbyBoardAPI, slug)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		ashbyBreaker.Record(false)
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentBot)
	req.Header.Set("Accept", "application/json")

	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		return engine.Cfg.HTTPClient.Do(req) //nolint:gosec // ATS API URL from argument, intentional outbound request
	})
	if err != nil {
		ashbyBreaker.Record(false)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		ashbyBreaker.Record(true) // 404 = valid response, not a failure
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		ashbyBreaker.Record(false)
		return nil, fmt.Errorf("ashby API status %d for %s", resp.StatusCode, slug)
	}

	// Use 5MB limit: large boards (Notion, OpenAI) can approach 2MB.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
	if err != nil {
		ashbyBreaker.Record(false)
		return nil, err
	}

	var ar ashbyResponse
	if err := json.Unmarshal(body, &ar); err != nil {
		ashbyBreaker.Record(false)
		return nil, fmt.Errorf("ashby parse: %w", err)
	}
	ashbyBreaker.Record(true)
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
	Platform           string `json:"platform"`             // "greenhouse"|"ashby"|"lever"
	Limit              int    `json:"limit,omitempty"`      // default 100, max 500
	Query              string `json:"query,omitempty"`      // optional case-insensitive title substring
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
			comp := ""
			if p.SalaryRange.Min > 0 {
				if p.SalaryRange.Max > p.SalaryRange.Min {
					comp = fmt.Sprintf("$%d-$%d %s", p.SalaryRange.Min, p.SalaryRange.Max, p.SalaryRange.Currency)
				} else {
					comp = fmt.Sprintf("$%d %s", p.SalaryRange.Min, p.SalaryRange.Currency)
				}
			}
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
