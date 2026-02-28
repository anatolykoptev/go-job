package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// NegotiationPoint is a single talking point for salary negotiation.
type NegotiationPoint struct {
	Point        string `json:"point"`
	Script       string `json:"script"`
	Anticipation string `json:"anticipation"`
	Response     string `json:"response"`
}

// NegotiationPrepResult is the structured output of negotiation_prep.
type NegotiationPrepResult struct {
	MarketData    string             `json:"market_data"`
	OpeningScript string             `json:"opening_script"`
	TalkingPoints []NegotiationPoint `json:"talking_points"`
	WalkAwayPoint string             `json:"walk_away_point"`
	ClosingScript string             `json:"closing_script"`
	RedFlags      []string           `json:"red_flags"`
	Summary       string             `json:"summary"`
}

const negotiationPrepPrompt = `You are an expert salary negotiation coach. Generate a complete negotiation playbook based on the candidate's situation.

ROLE: %s
CURRENT OFFER: %s
%s%s%s
Build a comprehensive negotiation strategy:

1. Market data context — summarize what the market pays for this role (use salary research data if provided).
2. Opening script — exact words to open the negotiation conversation (professional, confident, non-confrontational).
3. Talking points — 4-6 key arguments, each with:
   - The point to make
   - Exact script (what to say word-for-word)
   - Anticipated counter-argument from the employer
   - How to respond to that counter
4. Walk-away point (BATNA) — what's the candidate's best alternative? At what point should they walk away?
5. Closing script — how to accept gracefully once terms are agreed.
6. Red flags — signs the offer or company might be problematic.
7. Brief summary of the overall strategy.

Return a JSON object with this exact structure:
{
  "market_data": "<salary benchmarks and market context>",
  "opening_script": "<exact opening words for the negotiation, 3-4 sentences>",
  "talking_points": [
    {
      "point": "<argument summary>",
      "script": "<exact words to say>",
      "anticipation": "<likely employer counter>",
      "response": "<how to respond>"
    }
  ],
  "walk_away_point": "<BATNA analysis and walk-away threshold>",
  "closing_script": "<how to accept and close, 2-3 sentences>",
  "red_flags": ["<red flag 1>", "<red flag 2>"],
  "summary": "<overall negotiation strategy, 2-3 sentences>"
}

Return ONLY the JSON object, no markdown, no explanation.`

// PrepareNegotiation generates a salary negotiation playbook.
// If role+location provided, enriches with salary research data.
func PrepareNegotiation(ctx context.Context, role, company, location, currentOffer, targetComp, leverage string) (*NegotiationPrepResult, error) {
	// Optional salary research enrichment
	var salaryContext string
	if role != "" && location != "" {
		res, err := ResearchSalary(ctx, role, location, "")
		if err != nil {
			slog.Warn("negotiation_prep: salary research failed, proceeding without", slog.Any("error", err))
		} else {
			salaryContext = fmt.Sprintf("SALARY RESEARCH (%s in %s):\np25: %d, median: %d, p75: %d %s\nSources: %s\n\n",
				res.Role, res.Location, res.P25, res.Median, res.P75, res.Currency,
				strings.Join(res.Sources, ", "))
		}
	}

	var companyLine string
	if company != "" {
		companyLine = fmt.Sprintf("COMPANY: %s\n", company)
	}

	var targetLine string
	if targetComp != "" {
		targetLine = fmt.Sprintf("TARGET COMPENSATION: %s\n", targetComp)
	}

	var leverageLine string
	if leverage != "" {
		leverageLine = fmt.Sprintf("CANDIDATE LEVERAGE: %s\n", leverage)
	}

	prompt := fmt.Sprintf(negotiationPrepPrompt,
		role, currentOffer,
		companyLine, salaryContext,
		targetLine+leverageLine,
	)

	raw, err := engine.CallLLM(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("negotiation_prep LLM: %w", err)
	}

	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var result NegotiationPrepResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("negotiation_prep parse: %w (raw: %s)", err, engine.TruncateRunes(raw, 200, "..."))
	}
	return &result, nil
}
