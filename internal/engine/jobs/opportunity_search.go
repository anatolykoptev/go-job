package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// SearchOpportunities aggregates bounties, security programs, and freelance jobs
// into a unified Opportunity slice. Filters by type and query.
func SearchOpportunities(ctx context.Context, input engine.OpportunitySearchInput) (engine.OpportunitySearchOutput, error) {
	typ := strings.ToLower(input.Type)
	if typ == "" {
		typ = "all"
	}

	query := strings.ToLower(input.Query)

	var (
		mu   sync.Mutex
		opps []engine.Opportunity
	)

	var wg sync.WaitGroup

	if typ == "all" || typ == "bounty" {
		wg.Add(1)

		go func() {
			defer wg.Done()

			bounties := fetchAllBounties(ctx)
			converted := make([]engine.Opportunity, 0, len(bounties))

			for _, b := range bounties {
				converted = append(converted, bountyToOpportunity(b))
			}

			mu.Lock()
			opps = append(opps, converted...)
			mu.Unlock()
		}()
	}

	if typ == "all" || typ == "security" {
		wg.Add(1)

		go func() {
			defer wg.Done()

			programs := fetchAllSecurity(ctx)
			converted := make([]engine.Opportunity, 0, len(programs))

			for _, s := range programs {
				converted = append(converted, securityToOpportunity(s))
			}

			mu.Lock()
			opps = append(opps, converted...)
			mu.Unlock()
		}()
	}

	if typ == "all" || typ == "freelance" {
		wg.Add(1)

		go func() {
			defer wg.Done()

			jobs := fetchAllFreelance(ctx)
			converted := make([]engine.Opportunity, 0, len(jobs))

			for _, f := range jobs {
				converted = append(converted, freelanceToOpportunity(f))
			}

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
	var all []engine.BountyListing

	const perSourceLimit = 50

	if bvecs, err := SearchAlgoraEnriched(ctx, perSourceLimit); err == nil {
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
		bounties, err := s.fn(ctx, perSourceLimit)
		if err != nil {
			slog.Warn("opportunity_search: "+s.name+" error", slog.Any("error", err))
			continue
		}

		all = append(all, bounties...)
	}

	if len(all) > perSourceLimit {
		all = all[:perSourceLimit]
	}

	return all
}

func fetchAllSecurity(ctx context.Context) []engine.SecurityProgram {
	var all []engine.SecurityProgram

	const perSourceLimit = 50

	btd, err := SearchSecurityPrograms(ctx, perSourceLimit)
	if err != nil {
		slog.Warn("opportunity_search: security btd error", slog.Any("error", err))
	} else {
		all = append(all, btd...)
	}

	imm, err := SearchImmunefi(ctx, perSourceLimit)
	if err != nil {
		slog.Warn("opportunity_search: immunefi error", slog.Any("error", err))
	} else {
		all = append(all, imm...)
	}

	if len(all) > perSourceLimit {
		all = all[:perSourceLimit]
	}

	return all
}

func fetchAllFreelance(ctx context.Context) []engine.FreelanceJob {
	var all []engine.FreelanceJob

	const perSourceLimit = 30

	rok, err := SearchRemoteOKFreelance(ctx, "golang", perSourceLimit)
	if err != nil {
		slog.Warn("opportunity_search: remoteok error", slog.Any("error", err))
	} else {
		all = append(all, rok...)
	}

	him, err := SearchHimalayas(ctx, "golang", perSourceLimit)
	if err != nil {
		slog.Warn("opportunity_search: himalayas error", slog.Any("error", err))
	} else {
		all = append(all, him...)
	}

	const capFreelance = 50
	if len(all) > capFreelance {
		all = all[:capFreelance]
	}

	return all
}
