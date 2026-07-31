package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/hunt"
)

// maxIngestTotal is the hard safety ceiling on total items fetched across all
// sources during a scheduled ingest cycle (applyCap=false path). The per-source
// limit is 10000, but actual data is orders of magnitude smaller (BTD ~3000
// programs, bounty platforms <100 each). This cap prevents theoretical OOM if
// a source suddenly returns an unexpectedly large dataset, while being high
// enough to never affect normal operation.
const maxIngestTotal = 10000

// SearchOpportunities aggregates bounties, security programs, and freelance jobs
// into a unified Opportunity slice. Filters by type and query.
func SearchOpportunities(ctx context.Context, input engine.OpportunitySearchInput) (engine.OpportunitySearchOutput, error) {
	typ := strings.ToLower(input.Type)
	if typ == "" {
		typ = oppTypeAll
	}

	query := strings.ToLower(input.Query)

	var (
		mu   sync.Mutex
		opps []engine.Opportunity
	)

	var wg sync.WaitGroup

	if typ == oppTypeAll || typ == oppTypeBounty {
		wg.Add(1)

		go func() {
			defer wg.Done()

			bounties := fetchAllBounties(ctx)
			converted := make([]engine.Opportunity, 0, len(bounties))

			for _, b := range bounties {
				converted = append(converted, bountyToOpportunity(b))
			}

			PersistBounties(ctx, bounties)

			mu.Lock()
			opps = append(opps, converted...)
			mu.Unlock()
		}()
	}

	if typ == oppTypeAll || typ == oppTypeSecurity {
		wg.Add(1)

		go func() {
			defer wg.Done()

			programs := fetchAllSecurity(ctx)
			converted := make([]engine.Opportunity, 0, len(programs))

			for _, s := range programs {
				converted = append(converted, securityToOpportunity(s))
			}

			PersistSecurity(ctx, programs)

			mu.Lock()
			opps = append(opps, converted...)
			mu.Unlock()
		}()
	}

	if typ == oppTypeAll || typ == oppTypeFreelance {
		wg.Add(1)

		go func() {
			defer wg.Done()

			freelanceJobs := fetchAllFreelance(ctx)
			converted := make([]engine.Opportunity, 0, len(freelanceJobs))

			for _, f := range freelanceJobs {
				converted = append(converted, freelanceToOpportunity(f))
			}

			PersistFreelanceJobs(ctx, freelanceJobs)

			mu.Lock()
			opps = append(opps, converted...)
			mu.Unlock()
		}()
	}

	wg.Wait()

	if len(opps) == 0 {
		return engine.OpportunitySearchOutput{
			Query:   input.Query,
			Summary: "No opportunities found.",
		}, nil
	}

	if query != "" {
		opps = filterOpportunities(opps, query)
	}

	const maxResults = 100
	if len(opps) > maxResults {
		opps = opps[:maxResults]
	}

	return engine.OpportunitySearchOutput{
		Query:         input.Query,
		Opportunities: opps,
		Summary:       fmt.Sprintf("Found %d opportunities.", len(opps)),
	}, nil
}

func fetchAllBounties(ctx context.Context) []engine.BountyListing {
	items, _ := fetchAllBountiesImpl(ctx, 50, true)
	return items
}

// fetchAllBountiesImpl is the shared aggregator for both the on-demand search path
// (limit=50, applyCap=true) and the scheduled ingest path (limit=10000, applyCap=false).
// When applyCap is true the combined result is capped to limit items.
// Returns a SourceSummary mapping source name → row count for the cycle-complete log.
func fetchAllBountiesImpl(ctx context.Context, limit int, applyCap bool) ([]engine.BountyListing, SourceSummary) {
	var all []engine.BountyListing
	summary := make(SourceSummary)

	// Algora bounties removed from the fan-out on 2026-07-30: the public bounty
	// product is gone. The tRPC endpoint (console.algora.io/api/trpc/bounty.list)
	// still answers HTTP 200 but returns items:[] (zero bounties), and the HTML
	// scrape URL (algora.io/bounties) returns 404. Both paths failed silently
	// every cycle — the scrape logged "algora.io returned status 404" while the
	// tRPC API returned an empty slice that fell through to the dead scrape.
	// Rather than keep a dead source in the rotation, algora is removed from
	// both the scheduled ingest and the on-demand bounty search. The algora
	// fetch code (algora.go/algora_api.go/algora_enrich.go) is retained because
	// AnalyzeBounty (on-demand single-issue analysis) still references it.

	for _, s := range bountyFetchSources {
		bounties, err := s.fn(ctx, limit)
		outcome := classifySourceOutcome(len(bounties), err)
		engine.IncrHuntSourceOutcome("bounty", s.name, outcome)
		summary[s.name] = len(bounties)
		if err != nil {
			slog.Warn("opportunity_search: "+s.name+" error", slog.Any("error", err))
			continue
		}
		if len(bounties) > 0 {
			engine.SetHuntSourceLastSuccess("bounty", s.name)
		}
		all = append(all, bounties...)
	}

	if applyCap && len(all) > limit {
		all = all[:limit]
	} else if !applyCap && len(all) > maxIngestTotal {
		slog.Warn("opportunity_search: bounty ingest total exceeds safety cap, truncating",
			slog.Int("total", len(all)), slog.Int("cap", maxIngestTotal))
		all = all[:maxIngestTotal]
	}

	return all, summary
}

func fetchAllSecurity(ctx context.Context) []engine.SecurityProgram {
	items, _ := fetchAllSecurityImpl(ctx, 50, true)
	return items
}

// fetchAllSecurityImpl is the shared aggregator for both paths.
// When applyCap is false the per-source requests use a large limit and the
// BTD fetch goes direct (bypassing the result cache) so the ingest cycle
// always pulls the full live dataset. When applyCap is true the combined
// result is capped to limit items using the cached SearchSecurityPrograms path.
// Returns a SourceSummary mapping source name → row count for the cycle-complete log.
// Per-BTD-source outcomes are emitted inside fetchAllSecurityPrograms (the BTD
// aggregator); non-BTD source outcomes are emitted here.
func fetchAllSecurityImpl(ctx context.Context, limit int, applyCap bool) ([]engine.SecurityProgram, SourceSummary) {
	var all []engine.SecurityProgram
	summary := make(SourceSummary)

	if applyCap {
		// On-demand path: use cached helper with limit applied.
		btd, err := SearchSecurityPrograms(ctx, limit)
		// Per-BTD-source outcomes are emitted inside fetchAllSecurityPrograms
		// (called by SearchSecurityPrograms on cache miss). Here we only
		// track the aggregate BTD row count for the summary.
		if err != nil {
			slog.Warn("opportunity_search: security btd error", slog.Any("error", err))
		} else {
			all = append(all, btd...)
		}
		summary["btd"] = len(btd)
	} else {
		// Scheduled path: bypass cache to get the full live dataset.
		// fetchAllSecurityPrograms emits per-BTD-source outcomes + freshness
		// for each of the 5 BTD sources (hackerone, bugcrowd, intigriti,
		// yeswehack, federacy). We track the aggregate for the summary.
		btd, err := fetchAllSecurityPrograms(ctx)
		if err != nil {
			slog.Warn("opportunity_search: security btd error", slog.Any("error", err))
		} else {
			all = append(all, btd...)
		}
		summary["btd"] = len(btd)
	}

	for _, s := range securityFetchSources {
		programs, err := s.fn(ctx, limit)
		outcome := classifySourceOutcome(len(programs), err)
		engine.IncrHuntSourceOutcome("security", s.name, outcome)
		summary[s.name] = len(programs)
		if err != nil {
			slog.Warn("opportunity_search: "+s.name+" error", slog.Any("error", err))
			continue
		}
		if len(programs) > 0 {
			engine.SetHuntSourceLastSuccess("security", s.name)
		}
		all = append(all, programs...)
	}

	if applyCap && len(all) > limit {
		all = all[:limit]
	} else if !applyCap && len(all) > maxIngestTotal {
		slog.Warn("opportunity_search: security ingest total exceeds safety cap, truncating",
			slog.Int("total", len(all)), slog.Int("cap", maxIngestTotal))
		all = all[:maxIngestTotal]
	}

	return all, summary
}

func fetchAllFreelance(ctx context.Context) []engine.FreelanceJob {
	items, _ := fetchAllFreelanceImpl(ctx, 30, true)
	return items
}

// fetchAllFreelanceImpl is the shared aggregator for both paths.
// When applyCap is true the combined result is capped to 50 items (on-demand);
// when false no cap is applied (scheduled ingest).
// Returns a SourceSummary mapping source name → row count for the cycle-complete log.
func fetchAllFreelanceImpl(ctx context.Context, limit int, applyCap bool) ([]engine.FreelanceJob, SourceSummary) {
	var all []engine.FreelanceJob
	summary := make(SourceSummary)

	for _, s := range freelanceFetchSources {
		jobs, err := s.fn(ctx, limit)
		outcome := classifySourceOutcome(len(jobs), err)
		engine.IncrHuntSourceOutcome("freelance", s.name, outcome)
		summary[s.name] = len(jobs)
		if err != nil {
			slog.Warn("opportunity_search: "+s.name+" error", slog.Any("error", err))
			continue
		}
		if len(jobs) > 0 {
			engine.SetHuntSourceLastSuccess("freelance", s.name)
		}
		all = append(all, jobs...)
	}

	const capFreelance = 50
	if applyCap && len(all) > capFreelance {
		all = all[:capFreelance]
	} else if !applyCap && len(all) > maxIngestTotal {
		slog.Warn("opportunity_search: freelance ingest total exceeds safety cap, truncating",
			slog.Int("total", len(all)), slog.Int("cap", maxIngestTotal))
		all = all[:maxIngestTotal]
	}

	return all, summary
}

// PersistBounties writes BountyListings into the hunt store and applies the
// backfill-guard notify policy (best-effort, non-blocking on nil store).
// Notify logic lives here (not in UpsertBounty) so both the scheduled and
// on-demand paths share one policy, and the initial seed does not flood.
//
//nolint:dupl
func PersistBounties(ctx context.Context, bounties []engine.BountyListing) {
	store := engine.GetHuntStore()
	if store == nil {
		return
	}
	var created []hunt.Bounty
	fetched := len(bounties)
	for _, b := range bounties {
		hb := BountyListingToHunt(b)
		_, outcome, err := store.UpsertBounty(ctx, hb)
		engine.IncrHuntIngest(hunt.KindBounty, outcome.String())
		if err != nil {
			slog.Warn("hunt: upsert bounty failed", slog.Any("error", err))
			continue
		}
		if outcome == hunt.OutcomeCreated && hb.Status == hunt.StatusOpen {
			created = append(created, hb)
		}
	}
	notified, suppressed := applyBountyNotifyPolicy(store.Notifier(), created)
	slog.Info("hunt: persist bounties",
		slog.Int("fetched", fetched),
		slog.Int("created", len(created)),
		slog.Int("notified", notified),
		slog.Int("suppressed", suppressed),
	)
}

// PersistSecurity writes SecurityPrograms into the hunt store and applies the
// backfill-guard notify policy.
//
//nolint:dupl
func PersistSecurity(ctx context.Context, programs []engine.SecurityProgram) {
	store := engine.GetHuntStore()
	if store == nil {
		return
	}
	var created []hunt.Security
	fetched := len(programs)
	for _, sp := range programs {
		hs := SecurityProgramToHunt(sp)
		_, outcome, err := store.UpsertSecurity(ctx, hs)
		engine.IncrHuntIngest(hunt.KindSecurity, outcome.String())
		if err != nil {
			slog.Warn("hunt: upsert security failed", slog.Any("error", err))
			continue
		}
		if outcome == hunt.OutcomeCreated && hs.Status == hunt.StatusOpen {
			created = append(created, hs)
		}
	}
	notified, suppressed := applySecurityNotifyPolicy(store.Notifier(), created)
	slog.Info("hunt: persist security",
		slog.Int("fetched", fetched),
		slog.Int("created", len(created)),
		slog.Int("notified", notified),
		slog.Int("suppressed", suppressed),
	)
}

// PersistFreelanceJobs writes FreelanceJobs (remoteok/himalayas) into the hunt store
// and applies the backfill-guard notify policy.
//
//nolint:dupl
func PersistFreelanceJobs(ctx context.Context, freelanceJobs []engine.FreelanceJob) {
	store := engine.GetHuntStore()
	if store == nil {
		return
	}
	var created []hunt.Freelance
	fetched := len(freelanceJobs)
	for _, f := range freelanceJobs {
		hf := FreelanceJobToHunt(f)
		_, outcome, err := store.UpsertFreelance(ctx, hf)
		engine.IncrHuntIngest(hunt.KindFreelance, outcome.String())
		if err != nil {
			slog.Warn("hunt: upsert freelance failed", slog.Any("error", err))
			continue
		}
		if outcome == hunt.OutcomeCreated && hf.Status == hunt.StatusOpen {
			created = append(created, hf)
		}
	}
	notified, suppressed := applyFreelanceNotifyPolicy(store.Notifier(), created)
	slog.Info("hunt: persist freelance",
		slog.Int("fetched", fetched),
		slog.Int("created", len(created)),
		slog.Int("notified", notified),
		slog.Int("suppressed", suppressed),
	)
}
