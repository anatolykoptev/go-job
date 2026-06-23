package connectors

import (
	"context"
	"log/slog"
	"os"
	"strconv"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/anatolykoptev/go_job/internal/engine/sources"
)

// RawLinkedInFetcher is implemented by the LinkedIn adapter to expose
// the raw job list for post-processing enrichment (company/location/posted fields).
type RawLinkedInFetcher interface {
	FetchRaw(ctx context.Context, q Query) (results []engine.SearxngResult, liJobs []jobs.LinkedInJob, err error)
}

// Group name constants used in adapter Groups() methods.
// groupAll is the canonical sentinel; it lives in registry.go.
const (
	groupATS     = "ats"
	groupStartup = "startup"
	groupRemote  = "remote"
	groupUN      = "un"
)

// ----- per-connector default limits -----

const (
	defaultLinkedInLimit   = 50
	defaultATSLimit        = 10
	defaultHNLimit         = 20
	defaultIndeedLimit     = 15
	defaultHabrLimit       = 10
	defaultTwitterLimit    = 30
	defaultCraigslistLimit = 15
	defaultRemoteLimit     = 15
	defaultFreelancerLimit = 10
)

// ----- LinkedIn -----

type linkedInSource struct{}

func (linkedInSource) Name() string           { return "linkedin" }
func (linkedInSource) Capabilities() Capability { return 0 }
func (linkedInSource) Groups() []string         { return []string{groupAll} }
func (linkedInSource) SiteScope() string        { return "site:linkedin.com/jobs" }

func (linkedInSource) Fetch(ctx context.Context, q Query) ([]engine.SearxngResult, error) {
	results, _, err := linkedInSource{}.FetchRaw(ctx, q)
	return results, err
}

func (linkedInSource) FetchRaw(ctx context.Context, q Query) ([]engine.SearxngResult, []jobs.LinkedInJob, error) {
	liJobs, err := jobs.SearchLinkedInJobs(ctx, q.Query, q.Location, q.Experience, q.JobType, q.Remote, q.TimeRange, q.Salary, defaultLinkedInLimit, q.EasyApply)
	if err != nil {
		return nil, nil, err
	}
	results := jobs.LinkedInJobsToSearxngResults(ctx, liJobs, 8)
	return results, liJobs, nil
}

// ----- ATSSource — shared building block for Greenhouse / Lever / Ashby -----
//
// All three ATS providers share the same mechanism: discover company slugs via
// discoverJobURLs (go-search primary + DIRECT fallback), then fan-out to the
// provider's public JSON board API per slug.  The per-provider breakers and the
// shared atsLimiter live in jobs/ats.go and are preserved verbatim; only the
// dispatch is unified here.
//
// Lever-specific: dual-query secondary discovery (site-scope-first fallback)
// fires only for provider=="lever" — preserved exactly as in the original
// SearchLeverJobs implementation.

// atsProvider holds the static descriptor for one ATS provider.
type atsProvider struct {
	name      string   // connector name e.g. "greenhouse"
	groups    []string // e.g. []string{groupAll, groupATS, groupStartup}
	siteScope string   // e.g. "site:boards.greenhouse.io"
	// fetch delegates to the provider-specific Search* function in jobs/ats.go.
	fetch func(ctx context.Context, query, location string, limit int) ([]engine.SearxngResult, error)
	// queryVariants generates N distinct query strings for discovery fan-out.
	// Each entry is a func(base, location string) string.
	queryVariants []func(base, location string) string
}

// getConnectorQueryVariants returns the number of query variants to use.
// Reads DISCOVERY_QUERY_VARIANTS (range 1–5, default 3); mirrors ats.go logic.
func getConnectorQueryVariants() int {
	if v := os.Getenv("DISCOVERY_QUERY_VARIANTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 5 {
			return n
		}
	}
	return 3
}

// atsProviders is the static table of all three ATS connectors.
//
//nolint:gochecknoglobals // package-level init-once table, never mutated
var atsProviders = map[string]atsProvider{
	"greenhouse": {
		name:      "greenhouse",
		groups:    []string{groupAll, groupATS, groupStartup},
		siteScope: "site:boards.greenhouse.io",
		fetch:     jobs.SearchGreenhouseJobs,
		queryVariants: []func(base, location string) string{
			func(base, loc string) string {
				if loc != "" {
					return base + " " + loc + " site:boards.greenhouse.io"
				}
				return base + " site:boards.greenhouse.io"
			},
			func(base, loc string) string {
				if loc != "" {
					return "site:boards.greenhouse.io " + base + " " + loc
				}
				return "site:boards.greenhouse.io " + base
			},
			func(base, loc string) string {
				if loc != "" {
					return "senior " + base + " site:boards.greenhouse.io " + loc
				}
				return "senior " + base + " site:boards.greenhouse.io"
			},
		},
	},
	"lever": {
		name:      "lever",
		groups:    []string{groupAll, groupATS, groupStartup},
		siteScope: "site:jobs.lever.co",
		fetch:     jobs.SearchLeverJobs,
		queryVariants: []func(base, location string) string{
			func(base, loc string) string {
				if loc != "" {
					return base + " " + loc + " site:jobs.lever.co"
				}
				return base + " site:jobs.lever.co"
			},
			func(base, loc string) string {
				if loc != "" {
					return "site:jobs.lever.co " + base + " " + loc
				}
				return "site:jobs.lever.co " + base
			},
			func(base, loc string) string {
				if loc != "" {
					return "senior " + base + " site:jobs.lever.co " + loc
				}
				return "senior " + base + " site:jobs.lever.co"
			},
		},
	},
	"ashby": {
		name:      "ashby",
		groups:    []string{groupAll, groupATS, groupStartup},
		siteScope: "site:jobs.ashbyhq.com",
		fetch:     jobs.SearchAshbyJobs,
		queryVariants: []func(base, location string) string{
			func(base, loc string) string {
				if loc != "" {
					return base + " " + loc + " site:jobs.ashbyhq.com"
				}
				return base + " site:jobs.ashbyhq.com"
			},
			func(base, loc string) string {
				if loc != "" {
					return "site:jobs.ashbyhq.com " + base + " " + loc
				}
				return "site:jobs.ashbyhq.com " + base
			},
			func(base, loc string) string {
				if loc != "" {
					return "senior " + base + " site:jobs.ashbyhq.com " + loc
				}
				return "senior " + base + " site:jobs.ashbyhq.com"
			},
		},
	},
}

// atsSource is the single parametrized source struct backing all three ATS connectors.
type atsSource struct {
	p atsProvider
}

// ATSSource returns a Source backed by the named ATS provider.
// Panics on unknown provider name (init-time guard; same pattern as Registry.Register).
func ATSSource(provider string) Source {
	p, ok := atsProviders[provider]
	if !ok {
		panic("connectors: unknown ATS provider: " + provider)
	}
	return atsSource{p: p}
}

func (s atsSource) Name() string           { return s.p.name }
func (s atsSource) Capabilities() Capability { return 0 }
func (s atsSource) Groups() []string         { return s.p.groups }
func (s atsSource) SiteScope() string        { return s.p.siteScope }

func (s atsSource) Fetch(ctx context.Context, q Query) ([]engine.SearxngResult, error) {
	return s.p.fetch(ctx, q.Query, q.Location, defaultATSLimit)
}

// QueryVariants returns up to getConnectorQueryVariants() distinct query strings
// for the given base query and optional location. Used by consumers that want to
// fan out multiple queries for better slug-discovery yield (P1).
// Returns nil for providers with no variants configured (non-ATS sources).
func (s atsSource) QueryVariants(base, location string) []string {
	n := getConnectorQueryVariants()
	variants := s.p.queryVariants
	if len(variants) == 0 {
		return nil
	}
	if n < len(variants) {
		variants = variants[:n]
	}
	result := make([]string, 0, len(variants))
	for _, fn := range variants {
		result = append(result, fn(base, location))
	}
	return result
}

// ----- YC -----

type ycSource struct{}

func (ycSource) Name() string           { return "yc" }
func (ycSource) Capabilities() Capability { return 0 }
func (ycSource) Groups() []string         { return []string{groupAll, groupStartup} }
func (ycSource) SiteScope() string        { return "site:workatastartup.com" }

func (ycSource) Fetch(ctx context.Context, q Query) ([]engine.SearxngResult, error) {
	return jobs.SearchYCJobs(ctx, q.Query, q.Location, defaultATSLimit)
}

// ----- HN -----

type hnSource struct{}

func (hnSource) Name() string           { return "hn" }
func (hnSource) Capabilities() Capability { return 0 }
func (hnSource) Groups() []string         { return []string{groupAll, groupStartup} }
func (hnSource) SiteScope() string        { return "site:news.ycombinator.com \"who is hiring\"" }

func (hnSource) Fetch(ctx context.Context, q Query) ([]engine.SearxngResult, error) {
	return jobs.SearchHNJobs(ctx, q.Query, defaultHNLimit)
}

// ----- Indeed -----

type indeedSource struct{}

func (indeedSource) Name() string           { return "indeed" }
func (indeedSource) Capabilities() Capability { return NeedsAPIKey }
func (indeedSource) Groups() []string         { return []string{groupAll} }
func (indeedSource) SiteScope() string        { return "site:indeed.com" }

func (indeedSource) Fetch(ctx context.Context, q Query) ([]engine.SearxngResult, error) {
	return jobs.SearchIndeedJobsFiltered(ctx, q.Query, q.Location, q.JobType, q.TimeRange, defaultIndeedLimit)
}

// ----- Habr -----

type habrSource struct{}

func (habrSource) Name() string           { return "habr" }
func (habrSource) Capabilities() Capability { return 0 }
func (habrSource) Groups() []string         { return []string{groupAll} }
func (habrSource) SiteScope() string        { return "site:career.habr.com" }

func (habrSource) Fetch(ctx context.Context, q Query) ([]engine.SearxngResult, error) {
	return jobs.SearchHabrJobs(ctx, q.Query, q.Location, defaultHabrLimit)
}

// ----- Twitter -----

type twitterSource struct{}

func (twitterSource) Name() string           { return "twitter" }
func (twitterSource) Capabilities() Capability { return 0 }
func (twitterSource) Groups() []string         { return []string{groupAll} }
func (twitterSource) SiteScope() string        { return "site:twitter.com" }

func (twitterSource) Fetch(ctx context.Context, q Query) ([]engine.SearxngResult, error) {
	return jobs.SearchTwitterJobs(ctx, q.Query, defaultTwitterLimit)
}

// ----- Craigslist -----

type craigslistSource struct{}

func (craigslistSource) Name() string           { return "craigslist" }
func (craigslistSource) Capabilities() Capability { return 0 }
func (craigslistSource) Groups() []string         { return []string{groupAll} }
func (craigslistSource) SiteScope() string        { return "site:craigslist.org" }

func (craigslistSource) Fetch(ctx context.Context, q Query) ([]engine.SearxngResult, error) {
	return jobs.SearchCraigslistJobs(ctx, q.Query, q.Location, defaultCraigslistLimit)
}

// ----- KeylessJSONSource — shared building block for RemoteOK / WeWorkRemotely / Remotive -----
//
// All three share the same mechanism: GET a JSON/RSS endpoint (no auth required),
// unmarshal into []engine.RemoteJobListing via a provider-specific decode function,
// then convert to []engine.SearxngResult via the shared RemoteJobsToSearxngResults.
// Each provider keeps its own endpoint, limit, and decode logic — only the
// fetch→convert skeleton is unified.
//
// habr and freelancer are NOT collapsed here: habr returns a custom struct (not
// RemoteJobListing) and freelancer delegates to sources.SearchFreelancerAPI.
// Both reuse ErrParse and the shared fetch pattern in their own connectors
// (mechanism C, non-RemoteJobListing path) — premature abstraction on a
// 1-instance body is avoided per the 3rd-duplicate rule.

// keylessProvider holds the static descriptor for one keyless-JSON-API connector.
type keylessProvider struct {
	name      string
	groups    []string
	siteScope string
	// fetch delegates to the provider-specific Search* function in jobs/remotejobs.go.
	fetch func(ctx context.Context, query string, limit int) ([]engine.RemoteJobListing, error)
}

// keylessProviders is the static table of the three collapsed remote connectors.
//
//nolint:gochecknoglobals // package-level init-once table, never mutated
var keylessProviders = map[string]keylessProvider{
	"remoteok": {
		name:      "remoteok",
		groups:    []string{groupAll, groupRemote},
		siteScope: "site:remoteok.com",
		fetch:     jobs.SearchRemoteOK,
	},
	"weworkremotely": {
		name:      "weworkremotely",
		groups:    []string{groupAll, groupRemote},
		siteScope: "site:weworkremotely.com",
		fetch:     jobs.SearchWeWorkRemotely,
	},
	"remotive": {
		name:      "remotive",
		groups:    []string{groupAll, groupRemote},
		siteScope: "site:remotive.com",
		fetch:     jobs.SearchRemotive,
	},
}

// keylessJSONSource is the single parametrized source struct backing all three remote connectors.
type keylessJSONSource struct {
	p keylessProvider
}

// KeylessJSONSource returns a Source backed by the named keyless-JSON-API provider.
// Panics on unknown provider name (init-time guard).
func KeylessJSONSource(provider string) Source {
	p, ok := keylessProviders[provider]
	if !ok {
		panic("connectors: unknown keyless provider: " + provider)
	}
	return keylessJSONSource{p: p}
}

func (s keylessJSONSource) Name() string           { return s.p.name }
func (s keylessJSONSource) Capabilities() Capability { return 0 }
func (s keylessJSONSource) Groups() []string         { return s.p.groups }
func (s keylessJSONSource) SiteScope() string        { return s.p.siteScope }

func (s keylessJSONSource) Fetch(ctx context.Context, q Query) ([]engine.SearxngResult, error) {
	rjobs, err := s.p.fetch(ctx, q.Query, defaultRemoteLimit)
	if err != nil {
		return nil, err
	}
	return jobs.RemoteJobsToSearxngResults(rjobs), nil
}

// ----- Freelancer -----

type freelancerSource struct{}

func (freelancerSource) Name() string           { return "freelancer" }
func (freelancerSource) Capabilities() Capability { return 0 }
func (freelancerSource) Groups() []string         { return []string{groupAll} }
func (freelancerSource) SiteScope() string        { return "site:freelancer.com/projects" }

func (freelancerSource) Fetch(ctx context.Context, q Query) ([]engine.SearxngResult, error) {
	projects, err := sources.SearchFreelancerAPI(ctx, q.Query, defaultFreelancerLimit)
	return sources.FreelancerProjectsToSearxngResults(projects), err
}

// ----- Google -----

type googleSource struct{}

func (googleSource) Name() string           { return "google" }
func (googleSource) Capabilities() Capability { return 0 }
func (googleSource) Groups() []string         { return []string{groupAll} }
func (googleSource) SiteScope() string        { return "site:careers.google.com OR site:jobs.google.com" }

func (googleSource) Fetch(ctx context.Context, q Query) ([]engine.SearxngResult, error) {
	searxQuery := q.Query + " " + q.Location + " " + googleSource{}.SiteScope()
	results := engine.SearchDirect(ctx, searxQuery, q.Language)
	searx, err := engine.SearchSearXNG(ctx, searxQuery, q.Language, q.TimeRange, engine.DefaultSearchEngine)
	if err != nil {
		slog.Warn("connectors: google searxng error (additive)", slog.Any("error", err))
	}
	results = append(results, searx...)
	// DIRECT is authoritative; additive SearXNG err is intentionally not propagated.
	return results, nil
}

// ----- Inspira (UN Secretariat) -----

type inspiraSource struct{}

func (inspiraSource) Name() string           { return "inspira" }
func (inspiraSource) Capabilities() Capability { return OptIn }
func (inspiraSource) Groups() []string         { return []string{groupUN} }
func (inspiraSource) SiteScope() string        { return "site:careers.un.org" }

func (inspiraSource) Fetch(ctx context.Context, q Query) ([]engine.SearxngResult, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 15
	}
	return jobs.SearchInspiraJobs(ctx, q.Query, q.Location, limit)
}

// ----- UNDP -----

type undpSource struct{}

func (undpSource) Name() string           { return "undp" }
func (undpSource) Capabilities() Capability { return OptIn }
func (undpSource) Groups() []string         { return []string{groupUN} }
func (undpSource) SiteScope() string        { return "site:jobs.undp.org OR site:estm.fa.em2.oraclecloud.com" }

func (undpSource) Fetch(ctx context.Context, q Query) ([]engine.SearxngResult, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 15
	}
	return jobs.SearchUNDPJobs(ctx, q.Query, q.Location, limit)
}

// HasRequiredAPIKey reports whether the source's required API key is currently
// configured. Returns true for sources that do not require a key (NeedsAPIKey==0).
//
// Currently only "indeed" has NeedsAPIKey; the key is engine.Cfg.IndeedAPIKey.
// When NeedsAPIKey is wired at the runSource level, a source that returns false
// here will emit outcome=no_key without calling Fetch — saving the doomed API
// round-trip.
func HasRequiredAPIKey(src Source) bool {
	if src.Capabilities()&NeedsAPIKey == 0 {
		return true // key not required
	}
	if src.Name() == "indeed" {
		return engine.Cfg.IndeedAPIKey != ""
	}
	// Unknown NeedsAPIKey source: assume key absent so it emits no_key
	// rather than attempting a doomed call.
	return false
}

// BuildDefaultRegistry registers all 17 connectors in the same order as the
// original selectSources() order slice, preserving deterministic fan-out.
//
//nolint:funlen // registry init — one line per source
func BuildDefaultRegistry() *Registry {
	r := New()
	r.Register(linkedInSource{})
	r.Register(ATSSource("greenhouse"))
	r.Register(ATSSource("lever"))
	r.Register(ATSSource("ashby"))
	r.Register(ycSource{})
	r.Register(hnSource{})
	r.Register(indeedSource{})
	r.Register(habrSource{})
	r.Register(twitterSource{})
	r.Register(craigslistSource{})
	r.Register(KeylessJSONSource("remoteok"))
	r.Register(KeylessJSONSource("weworkremotely"))
	r.Register(KeylessJSONSource("remotive"))
	r.Register(freelancerSource{})
	r.Register(googleSource{})
	r.Register(inspiraSource{})
	r.Register(undpSource{})
	return r
}
