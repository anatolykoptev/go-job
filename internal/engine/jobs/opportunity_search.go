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
	return fetchAllBountiesImpl(ctx, 50, true)
}

// fetchAllBountiesImpl is the shared aggregator for both the on-demand search path
// (limit=50, applyCap=true) and the scheduled ingest path (limit=10000, applyCap=false).
// When applyCap is true the combined result is capped to limit items.
func fetchAllBountiesImpl(ctx context.Context, limit int, applyCap bool) []engine.BountyListing {
	var all []engine.BountyListing

	if bvecs, err := SearchAlgoraEnriched(ctx, limit); err == nil {
		for _, bv := range bvecs {
			all = append(all, bv.Bounty)
		}
	} else {
		slog.Warn("opportunity_search: algora error", slog.Any("error", err))
	}

	sources := []struct {
		name string
		fn   func(context.Context, int) ([]engine.BountyListing, error)
	}{
		{"opire", SearchOpire},
		{"bountyhub", SearchBountyHub},
		{"boss", SearchBoss},
		{"lightning", SearchLightning},
		{"collaborators", SearchCollaborators},
	}

	for _, s := range sources {
		bounties, err := s.fn(ctx, limit)
		if err != nil {
			slog.Warn("opportunity_search: "+s.name+" error", slog.Any("error", err))
			continue
		}

		all = append(all, bounties...)
	}

	if applyCap && len(all) > limit {
		all = all[:limit]
	}

	return all
}

func fetchAllSecurity(ctx context.Context) []engine.SecurityProgram {
	return fetchAllSecurityImpl(ctx, 50, true)
}

// fetchAllSecurityImpl is the shared aggregator for both paths.
// When applyCap is false the per-source requests use a large limit and the
// BTD fetch goes direct (bypassing the result cache) so the ingest cycle
// always pulls the full live dataset. When applyCap is true the combined
// result is capped to limit items using the cached SearchSecurityPrograms path.
func fetchAllSecurityImpl(ctx context.Context, limit int, applyCap bool) []engine.SecurityProgram {
	var all []engine.SecurityProgram

	if applyCap {
		// On-demand path: use cached helper with limit applied.
		btd, err := SearchSecurityPrograms(ctx, limit)
		if err != nil {
			slog.Warn("opportunity_search: security btd error", slog.Any("error", err))
		} else {
			all = append(all, btd...)
		}
	} else {
		// Scheduled path: bypass cache to get the full live dataset.
		btd, err := fetchAllSecurityPrograms(ctx)
		if err != nil {
			slog.Warn("opportunity_search: security btd error", slog.Any("error", err))
		} else {
			all = append(all, btd...)
		}
	}

	imm, err := SearchImmunefi(ctx, limit)
	if err != nil {
		slog.Warn("opportunity_search: immunefi error", slog.Any("error", err))
	} else {
		all = append(all, imm...)
	}

	shr, err := SearchSherlock(ctx, limit)
	if err != nil {
		slog.Warn("opportunity_search: sherlock error", slog.Any("error", err))
	} else {
		all = append(all, shr...)
	}

	cantina, err := SearchCantina(ctx, limit)
	if err != nil {
		slog.Warn("opportunity_search: cantina error", slog.Any("error", err))
	} else {
		all = append(all, cantina...)
	}

	c4r, err := SearchCode4rena(ctx, limit)
	if err != nil {
		slog.Warn("opportunity_search: code4rena error", slog.Any("error", err))
	} else {
		all = append(all, c4r...)
	}

	if applyCap && len(all) > limit {
		all = all[:limit]
	}

	return all
}

func fetchAllFreelance(ctx context.Context) []engine.FreelanceJob {
	return fetchAllFreelanceImpl(ctx, 30, true)
}

// fetchAllFreelanceImpl is the shared aggregator for both paths.
// When applyCap is true the combined result is capped to 50 items (on-demand);
// when false no cap is applied (scheduled ingest).
func fetchAllFreelanceImpl(ctx context.Context, limit int, applyCap bool) []engine.FreelanceJob {
	var all []engine.FreelanceJob

	rok, err := SearchRemoteOKFreelance(ctx, langAliasGolang, limit)
	if err != nil {
		slog.Warn("opportunity_search: remoteok error", slog.Any("error", err))
	} else {
		all = append(all, rok...)
	}

	him, err := SearchHimalayas(ctx, langAliasGolang, limit)
	if err != nil {
		slog.Warn("opportunity_search: himalayas error", slog.Any("error", err))
	} else {
		all = append(all, him...)
	}

	const capFreelance = 50
	if applyCap && len(all) > capFreelance {
		all = all[:capFreelance]
	}

	return all
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
