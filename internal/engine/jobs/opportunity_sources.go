package jobs

import (
	"context"
	"errors"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// This file defines the enumerable source tables for the three scheduled ingest
// fan-outs (bounty, security, freelance). Each table is the SINGLE source of
// truth for both the fan-out iteration AND the metric source-label set — a new
// source added to a table is automatically picked up by the fan-out, the
// metric pre-touch (warmAlertBoundedMetrics), and the FormatMetrics flat-text
// endpoint, without touching any metric registration code.
//
// The BTD security sources (securitySources in security_bounty.go) are already
// a table; their names are derived via SecuritySourceNames() which reads that
// table directly. The non-BTD security sources, bounty sources, and freelance
// sources are defined here as package-level tables that the fan-out iterates.

// SourceSummary tracks per-source row counts for one fan-out cycle. Returned
// by the FetchAll*Unlimited functions so the opp worker can log per-source
// counts in the cycle-complete line (an operator reading the log can see
// which sources contributed without reading container logs by hand).
type SourceSummary map[string]int

// bountyFetchSources is the table of bounty sources in the scheduled fan-out.
var bountyFetchSources = []struct {
	name string
	fn   func(ctx context.Context, limit int) ([]engine.BountyListing, error)
}{
	{"opire", SearchOpire},
	{"bountyhub", SearchBountyHub},
	{"boss", SearchBoss},
	{"lightning", SearchLightning},
	{"collaborators", SearchCollaborators},
}

// securityFetchSources is the table of non-BTD security sources in the
// scheduled fan-out. The BTD sources (hackerone, bugcrowd, intigriti,
// yeswehack, federacy) are tracked separately via the securitySources table
// in security_bounty.go; their names are derived via SecuritySourceNames().
var securityFetchSources = []struct {
	name string
	fn   func(ctx context.Context, limit int) ([]engine.SecurityProgram, error)
}{
	{"immunefi", SearchImmunefi},
	{"sherlock", SearchSherlock},
	{"cantina", SearchCantina},
	{"code4rena", SearchCode4rena},
}

// freelanceFetchSources is the table of freelance sources in the scheduled
// fan-out. RemoteOK and Himalayas take a query parameter (langAliasGolang)
// which is bound in the closure so the table shape is uniform.
var freelanceFetchSources = []struct {
	name string
	fn   func(ctx context.Context, limit int) ([]engine.FreelanceJob, error)
}{
	{"remoteok", func(ctx context.Context, limit int) ([]engine.FreelanceJob, error) {
		return SearchRemoteOKFreelance(ctx, langAliasGolang, limit)
	}},
	{"himalayas", func(ctx context.Context, limit int) ([]engine.FreelanceJob, error) {
		return SearchHimalayas(ctx, langAliasGolang, limit)
	}},
}

// BountySourceNames returns the names of all bounty sources in the scheduled
// fan-out, derived from the bountyFetchSources table.
func BountySourceNames() []string {
	names := make([]string, 0, len(bountyFetchSources))
	for _, s := range bountyFetchSources {
		names = append(names, s.name)
	}
	return names
}

// SecuritySourceNames returns the names of all security sources in the
// scheduled fan-out, derived from the securitySources table (BTD, in
// security_bounty.go) + the securityFetchSources table (non-BTD).
func SecuritySourceNames() []string {
	names := make([]string, 0, len(securitySources)+len(securityFetchSources))
	for _, s := range securitySources {
		names = append(names, s.platform)
	}
	for _, s := range securityFetchSources {
		names = append(names, s.name)
	}
	return names
}

// FreelanceSourceNames returns the names of all freelance sources in the
// scheduled fan-out, derived from the freelanceFetchSources table.
func FreelanceSourceNames() []string {
	names := make([]string, 0, len(freelanceFetchSources))
	for _, s := range freelanceFetchSources {
		names = append(names, s.name)
	}
	return names
}

// classifySourceOutcome maps a fetch result (row count + error) to the bounded
// outcome label for gojob_hunt_source_outcome_total. Uses errors.Is(err,
// ErrParse) to distinguish parse_error from fetch_error — the distinction that
// would have pointed at the right layer for the hackerone truncated-read
// failure (reported as a parse failure but actually a fetch-level truncation).
func classifySourceOutcome(n int, err error) string {
	if err != nil {
		if errors.Is(err, ErrParse) {
			return "parse_error"
		}
		return "fetch_error"
	}
	if n > 0 {
		return "ok"
	}
	return "empty"
}

// init registers the per-kind source name lists with the engine package so
// the metric pre-touch (warmAlertBoundedMetrics) and FormatMetrics can derive
// the source label set from the real fan-out tables. This runs before main()
// (Go init order: engine is initialized first since jobs imports engine, then
// jobs.init() runs), so the lists are available when engine.Init() calls
// warmAlertBoundedMetrics().
func init() {
	engine.RegisterHuntSources(map[string][]string{
		"security":  SecuritySourceNames(),
		"bounty":    BountySourceNames(),
		"freelance": FreelanceSourceNames(),
	})
}
