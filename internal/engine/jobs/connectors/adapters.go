package connectors

//nolint:dupl // thin adapter structs — intentionally similar

import (
	"context"
	"log/slog"

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
const (
	groupALL     = "all"
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
	defaultGoogleLimit     = 0 // google uses direct+searxng, no limit param
	defaultInspiraLimit    = 0 // uses caller-supplied limit
	defaultUNDPLimit       = 0 // uses caller-supplied limit
)

// ----- LinkedIn -----

type linkedInSource struct{}

func (linkedInSource) Name() string          { return "linkedin" }
func (linkedInSource) Capabilities() Capability { return 0 }
func (linkedInSource) Groups() []string        { return []string{groupALL} }
func (linkedInSource) SiteScope() string       { return "site:linkedin.com/jobs" }

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

// ----- Greenhouse -----

type greenhouseSource struct{}

func (greenhouseSource) Name() string          { return "greenhouse" }
func (greenhouseSource) Capabilities() Capability { return 0 }
func (greenhouseSource) Groups() []string        { return []string{groupALL, groupATS, groupStartup} }
func (greenhouseSource) SiteScope() string       { return "site:boards.greenhouse.io" }

func (greenhouseSource) Fetch(ctx context.Context, q Query) ([]engine.SearxngResult, error) {
	return jobs.SearchGreenhouseJobs(ctx, q.Query, q.Location, defaultATSLimit)
}

// ----- Lever -----

type leverSource struct{}

func (leverSource) Name() string          { return "lever" }
func (leverSource) Capabilities() Capability { return 0 }
func (leverSource) Groups() []string        { return []string{groupALL, groupATS, groupStartup} }
func (leverSource) SiteScope() string       { return "site:jobs.lever.co" }

func (leverSource) Fetch(ctx context.Context, q Query) ([]engine.SearxngResult, error) {
	return jobs.SearchLeverJobs(ctx, q.Query, q.Location, defaultATSLimit)
}

// ----- Ashby -----

type ashbySource struct{}

func (ashbySource) Name() string          { return "ashby" }
func (ashbySource) Capabilities() Capability { return 0 }
func (ashbySource) Groups() []string        { return []string{groupALL, groupATS, groupStartup} }
func (ashbySource) SiteScope() string       { return "site:jobs.ashbyhq.com" }

func (ashbySource) Fetch(ctx context.Context, q Query) ([]engine.SearxngResult, error) {
	return jobs.SearchAshbyJobs(ctx, q.Query, q.Location, defaultATSLimit)
}

// ----- YC -----

type ycSource struct{}

func (ycSource) Name() string          { return "yc" }
func (ycSource) Capabilities() Capability { return 0 }
func (ycSource) Groups() []string        { return []string{groupALL, groupStartup} }
func (ycSource) SiteScope() string       { return "site:workatastartup.com" }

func (ycSource) Fetch(ctx context.Context, q Query) ([]engine.SearxngResult, error) {
	return jobs.SearchYCJobs(ctx, q.Query, q.Location, defaultATSLimit)
}

// ----- HN -----

type hnSource struct{}

func (hnSource) Name() string          { return "hn" }
func (hnSource) Capabilities() Capability { return 0 }
func (hnSource) Groups() []string        { return []string{groupALL, groupStartup} }
func (hnSource) SiteScope() string       { return "site:news.ycombinator.com \"who is hiring\"" }

func (hnSource) Fetch(ctx context.Context, q Query) ([]engine.SearxngResult, error) {
	return jobs.SearchHNJobs(ctx, q.Query, defaultHNLimit)
}

// ----- Indeed -----

type indeedSource struct{}

func (indeedSource) Name() string          { return "indeed" }
func (indeedSource) Capabilities() Capability { return NeedsAPIKey }
func (indeedSource) Groups() []string        { return []string{groupALL} }
func (indeedSource) SiteScope() string       { return "site:indeed.com" }

func (indeedSource) Fetch(ctx context.Context, q Query) ([]engine.SearxngResult, error) {
	return jobs.SearchIndeedJobsFiltered(ctx, q.Query, q.Location, q.JobType, q.TimeRange, defaultIndeedLimit)
}

// ----- Habr -----

type habrSource struct{}

func (habrSource) Name() string          { return "habr" }
func (habrSource) Capabilities() Capability { return 0 }
func (habrSource) Groups() []string        { return []string{groupALL} }
func (habrSource) SiteScope() string       { return "site:career.habr.com" }

func (habrSource) Fetch(ctx context.Context, q Query) ([]engine.SearxngResult, error) {
	return jobs.SearchHabrJobs(ctx, q.Query, q.Location, defaultHabrLimit)
}

// ----- Twitter -----

type twitterSource struct{}

func (twitterSource) Name() string          { return "twitter" }
func (twitterSource) Capabilities() Capability { return 0 }
func (twitterSource) Groups() []string        { return []string{groupALL} }
func (twitterSource) SiteScope() string       { return "site:twitter.com" }

func (twitterSource) Fetch(ctx context.Context, q Query) ([]engine.SearxngResult, error) {
	return jobs.SearchTwitterJobs(ctx, q.Query, defaultTwitterLimit)
}

// ----- Craigslist -----

type craigslistSource struct{}

func (craigslistSource) Name() string          { return "craigslist" }
func (craigslistSource) Capabilities() Capability { return 0 }
func (craigslistSource) Groups() []string        { return []string{groupALL} }
func (craigslistSource) SiteScope() string       { return "site:craigslist.org" }

func (craigslistSource) Fetch(ctx context.Context, q Query) ([]engine.SearxngResult, error) {
	return jobs.SearchCraigslistJobs(ctx, q.Query, q.Location, defaultCraigslistLimit)
}

// ----- RemoteOK -----

type remoteOKSource struct{}

func (remoteOKSource) Name() string          { return "remoteok" }
func (remoteOKSource) Capabilities() Capability { return 0 }
func (remoteOKSource) Groups() []string        { return []string{groupALL, groupRemote} }
func (remoteOKSource) SiteScope() string       { return "site:remoteok.com" }

func (remoteOKSource) Fetch(ctx context.Context, q Query) ([]engine.SearxngResult, error) {
	rjobs, err := jobs.SearchRemoteOK(ctx, q.Query, defaultRemoteLimit)
	return jobs.RemoteJobsToSearxngResults(rjobs), err
}

// ----- WeWorkRemotely -----

type wwrSource struct{}

func (wwrSource) Name() string          { return "weworkremotely" }
func (wwrSource) Capabilities() Capability { return 0 }
func (wwrSource) Groups() []string        { return []string{groupALL, groupRemote} }
func (wwrSource) SiteScope() string       { return "site:weworkremotely.com" }

func (wwrSource) Fetch(ctx context.Context, q Query) ([]engine.SearxngResult, error) {
	rjobs, err := jobs.SearchWeWorkRemotely(ctx, q.Query, defaultRemoteLimit)
	return jobs.RemoteJobsToSearxngResults(rjobs), err
}

// ----- Remotive -----

type remotiveSource struct{}

func (remotiveSource) Name() string          { return "remotive" }
func (remotiveSource) Capabilities() Capability { return 0 }
func (remotiveSource) Groups() []string        { return []string{groupALL, groupRemote} }
func (remotiveSource) SiteScope() string       { return "site:remotive.com" }

func (remotiveSource) Fetch(ctx context.Context, q Query) ([]engine.SearxngResult, error) {
	rjobs, err := jobs.SearchRemotive(ctx, q.Query, defaultRemoteLimit)
	return jobs.RemoteJobsToSearxngResults(rjobs), err
}

// ----- Freelancer -----

type freelancerSource struct{}

func (freelancerSource) Name() string          { return "freelancer" }
func (freelancerSource) Capabilities() Capability { return 0 }
func (freelancerSource) Groups() []string        { return []string{groupALL} }
func (freelancerSource) SiteScope() string       { return "site:freelancer.com/projects" }

func (freelancerSource) Fetch(ctx context.Context, q Query) ([]engine.SearxngResult, error) {
	projects, err := sources.SearchFreelancerAPI(ctx, q.Query, defaultFreelancerLimit)
	return sources.FreelancerProjectsToSearxngResults(projects), err
}

// ----- Google -----

type googleSource struct{}

func (googleSource) Name() string          { return "google" }
func (googleSource) Capabilities() Capability { return 0 }
func (googleSource) Groups() []string        { return []string{groupALL} }
func (googleSource) SiteScope() string       { return "site:careers.google.com OR site:jobs.google.com" }

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

func (inspiraSource) Name() string          { return "inspira" }
func (inspiraSource) Capabilities() Capability { return OptIn }
func (inspiraSource) Groups() []string        { return []string{groupUN} }
func (inspiraSource) SiteScope() string       { return "site:careers.un.org" }

func (inspiraSource) Fetch(ctx context.Context, q Query) ([]engine.SearxngResult, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 15
	}
	return jobs.SearchInspiraJobs(ctx, q.Query, q.Location, limit)
}

// ----- UNDP -----

type undpSource struct{}

func (undpSource) Name() string          { return "undp" }
func (undpSource) Capabilities() Capability { return OptIn }
func (undpSource) Groups() []string        { return []string{groupUN} }
func (undpSource) SiteScope() string       { return "site:jobs.undp.org OR site:estm.fa.em2.oraclecloud.com" }

func (undpSource) Fetch(ctx context.Context, q Query) ([]engine.SearxngResult, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 15
	}
	return jobs.SearchUNDPJobs(ctx, q.Query, q.Location, limit)
}

// BuildDefaultRegistry registers all 17 connectors in the same order as the
// original selectSources() order slice, preserving deterministic fan-out.
//
//nolint:funlen // registry init — one line per source
func BuildDefaultRegistry() *Registry {
	r := New()
	r.Register(linkedInSource{})
	r.Register(greenhouseSource{})
	r.Register(leverSource{})
	r.Register(ashbySource{})
	r.Register(ycSource{})
	r.Register(hnSource{})
	r.Register(indeedSource{})
	r.Register(habrSource{})
	r.Register(twitterSource{})
	r.Register(craigslistSource{})
	r.Register(remoteOKSource{})
	r.Register(wwrSource{})
	r.Register(remotiveSource{})
	r.Register(freelancerSource{})
	r.Register(googleSource{})
	r.Register(inspiraSource{})
	r.Register(undpSource{})
	return r
}
