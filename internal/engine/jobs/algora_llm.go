package jobs

import (
	"context"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// BountiesToSearxngResults converts bounty listings to engine.SearxngResult for LLM pipeline.
func BountiesToSearxngResults(bounties []engine.BountyListing) []engine.SearxngResult {
	results := make([]engine.SearxngResult, 0, len(bounties))
	for _, b := range bounties {
		var content strings.Builder
		content.WriteString("**Bounty:** " + b.Amount)
		content.WriteString(" | **Org:** " + b.Org)
		if b.IssueNum != "" {
			content.WriteString(" | **Issue:** " + b.IssueNum)
		}
		if len(b.Skills) > 0 {
			content.WriteString(" | **Skills:** " + strings.Join(b.Skills, ", "))
		}
		content.WriteString(" | **Source:** " + b.Source)

		results = append(results, engine.SearxngResult{
			Title:   b.Org + ": " + b.Title + " (" + b.Amount + ")",
			Content: content.String(),
			URL:     b.URL,
			Score:   1.0,
		})
	}
	return results
}

// llmBountyOutput is the JSON structure expected from the LLM for bounty search.
type llmBountyOutput struct {
	Bounties []engine.BountyListing `json:"bounties"`
	Summary  string                 `json:"summary"`
}

// SummarizeBountyResults calls the LLM with bounty-specific prompt and parses structured bounties.
func SummarizeBountyResults(ctx context.Context, query, instruction string, contentLimit int, results []engine.SearxngResult, contents map[string]string) (*engine.BountySearchOutput, error) {
	parsed, raw, err := engine.SummarizeToJSON[llmBountyOutput](ctx, query, instruction, contentLimit, results, contents)
	if err != nil {
		return nil, err
	}
	if parsed == nil {
		return &engine.BountySearchOutput{Query: query, Summary: raw}, nil
	}

	enriched := make([]engine.BountyListing, len(parsed.Bounties))
	for i, b := range parsed.Bounties {
		if b.URL == "" && i < len(results) {
			b.URL = results[i].URL
		}
		if b.Source == "" {
			b.Source = "algora"
		}
		enriched[i] = b
	}
	return &engine.BountySearchOutput{Query: query, Bounties: enriched, Summary: parsed.Summary}, nil
}
