package jobserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerBountySearch(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "bounty_search",
		Description: "Search for open-source bounties on Algora.io, Opire.dev, BountyHub.dev, Boss.dev, Lightning Bounties, and Collaborators.build. Returns paid GitHub issues with bounty amounts. Filter by technology, keyword, minimum amount, or required skills.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input engine.BountySearchInput) (*mcp.CallToolResult, engine.SmartSearchOutput, error) {
		// Load fully enriched bounties from cache (all enrichment done at cache time).
		bvecs, err := jobs.SearchAlgoraEnriched(ctx, 30)
		if err != nil {
			slog.Warn("bounty_search: algora error", slog.Any("error", err))
		}

		// Also fetch Opire bounties and merge.
		opireBounties, opireErr := jobs.SearchOpire(ctx, 30)
		if opireErr != nil {
			slog.Warn("bounty_search: opire error", slog.Any("error", opireErr))
		}
		for _, ob := range opireBounties {
			bvecs = append(bvecs, jobs.BountyWithVector{Bounty: ob})
		}

		// Also fetch BountyHub bounties and merge.
		bhBounties, bhErr := jobs.SearchBountyHub(ctx, 50)
		if bhErr != nil {
			slog.Warn("bounty_search: bountyhub error", slog.Any("error", bhErr))
		}
		for _, b := range bhBounties {
			bvecs = append(bvecs, jobs.BountyWithVector{Bounty: b})
		}

		// Also fetch Boss.dev bounties and merge.
		bossBounties, bossErr := jobs.SearchBoss(ctx, 50)
		if bossErr != nil {
			slog.Warn("bounty_search: boss error", slog.Any("error", bossErr))
		}
		for _, b := range bossBounties {
			bvecs = append(bvecs, jobs.BountyWithVector{Bounty: b})
		}

		// Also fetch Lightning Bounties and merge.
		lnBounties, lnErr := jobs.SearchLightning(ctx, 50)
		if lnErr != nil {
			slog.Warn("bounty_search: lightning error", slog.Any("error", lnErr))
		}
		for _, b := range lnBounties {
			bvecs = append(bvecs, jobs.BountyWithVector{Bounty: b})
		}

		// Also fetch Collaborators.build bounties and merge.
		collabBounties, collabErr := jobs.SearchCollaborators(ctx, 50)
		if collabErr != nil {
			slog.Warn("bounty_search: collaborators error", slog.Any("error", collabErr))
		}
		for _, b := range collabBounties {
			bvecs = append(bvecs, jobs.BountyWithVector{Bounty: b})
		}

		if len(bvecs) == 0 {
			if err != nil && opireErr != nil && bhErr != nil && bossErr != nil && lnErr != nil && collabErr != nil {
				return nil, engine.SmartSearchOutput{}, fmt.Errorf("bounty fetch failed: algora: %v; opire: %v; bountyhub: %v; boss: %v; lightning: %v; collaborators: %v", err, opireErr, bhErr, bossErr, lnErr, collabErr)
			}
			return bountyResult(engine.BountySearchOutput{Query: input.Query, Summary: "No bounties found."})
		}

		// Apply input filters (min_amount, skills).
		bvecs = filterBountyInputs(bvecs, input)
		if len(bvecs) == 0 {
			return bountyResult(engine.BountySearchOutput{
				Query:   input.Query,
				Summary: "No bounties match the specified filters.",
			})
		}

		// Try embedding pipeline (query only — bounty vectors are precomputed).
		embedClient := jobs.GetEmbedClient()
		hasVectors := len(bvecs) > 0 && len(bvecs[0].Vector) > 0
		if embedClient != nil && hasVectors {
			result, err := bountyEmbedPipeline(ctx, embedClient, input.Query, bvecs)
			if err != nil {
				slog.Warn("bounty_search: embed pipeline failed, falling back to LLM", slog.Any("error", err))
			} else {
				return bountyResult(result)
			}
		}

		// LLM fallback (uses old pipeline with FetchContentsParallel).
		bounties := make([]engine.BountyListing, len(bvecs))
		for i, bv := range bvecs {
			bounties[i] = bv.Bounty
		}
		searxResults := jobs.BountiesToSearxngResults(bounties)
		apiURLs := make(map[string]bool)
		contents := engine.FetchContentsParallel(ctx, searxResults, apiURLs)
		return bountyLLMFallback(ctx, input, bounties, searxResults, contents)
	})
}

// bountyLLMFallback uses the existing LLM summarization pipeline.
func bountyLLMFallback(ctx context.Context, input engine.BountySearchInput, bounties []engine.BountyListing, searxResults []engine.SearxngResult, contents map[string]string) (*mcp.CallToolResult, engine.SmartSearchOutput, error) {
	bountyOut, err := jobs.SummarizeBountyResults(ctx, input.Query, engine.BountySearchInstruction, 4000, searxResults, contents)
	if err != nil {
		slog.Warn("bounty_search: LLM summarization failed, returning raw", slog.Any("error", err))
		out := engine.BountySearchOutput{
			Query:    input.Query,
			Bounties: bounties,
			Summary:  fmt.Sprintf("Found %d bounties (LLM summary unavailable).", len(bounties)),
		}
		return bountyResult(out)
	}

	for i := range bountyOut.Bounties {
		b := &bountyOut.Bounties[i]
		if b.URL == "" && i < len(searxResults) {
			b.URL = searxResults[i].URL
		}
		if b.Source == "" {
			b.Source = "algora"
		}
	}
	return bountyResult(*bountyOut)
}

func bountyResult(out engine.BountySearchOutput) (*mcp.CallToolResult, engine.SmartSearchOutput, error) {
	jsonBytes, err := json.Marshal(out)
	if err != nil {
		return nil, engine.SmartSearchOutput{}, errors.New("json marshal failed")
	}
	result := engine.SmartSearchOutput{
		Query:   out.Query,
		Answer:  string(jsonBytes),
		Sources: []engine.SourceItem{},
	}
	return nil, result, nil
}
