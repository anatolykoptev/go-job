package jobs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// OfferItem is a single offer in a comparison.
type OfferItem struct {
	Company   string   `json:"company"`
	Role      string   `json:"role"`
	TotalComp string   `json:"total_comp"`
	Pros      []string `json:"pros"`
	Cons      []string `json:"cons"`
	Score     int      `json:"score"`
}

// OfferCompareResult is the structured output of offer_compare.
type OfferCompareResult struct {
	Offers         []OfferItem `json:"offers"`
	Recommendation string      `json:"recommendation"`
	Comparison     string      `json:"comparison"`
	Summary        string      `json:"summary"`
}

const offerComparePrompt = `You are an expert career advisor specializing in job offer evaluation and comparison.

Analyze and compare the following job offers. Consider all dimensions: compensation (base, equity, bonus), benefits (health, PTO, 401k), work-life balance, growth potential, company stability, remote policy, location, and career trajectory.

OFFERS:
%s
%s
Provide a thorough comparison:

1. For each offer, calculate the estimated total annual compensation (base + equity/year + bonus) and list pros and cons.
2. Score each offer 0-100 based on overall value (not just salary).
3. Write a side-by-side comparison covering: compensation, benefits, WLB, growth, stability.
4. Give a clear recommendation with reasoning.
5. Write a brief summary.

Return a JSON object with this exact structure:
{
  "offers": [
    {
      "company": "<company name>",
      "role": "<role title>",
      "total_comp": "<estimated total annual compensation>",
      "pros": ["<pro 1>", "<pro 2>"],
      "cons": ["<con 1>", "<con 2>"],
      "score": <0-100>
    }
  ],
  "recommendation": "<which offer to accept and why, 2-3 sentences>",
  "comparison": "<detailed side-by-side analysis, 4-6 sentences covering comp, benefits, WLB, growth>",
  "summary": "<1-2 sentence bottom line>"
}

Return ONLY the JSON object, no markdown, no explanation.`

// CompareOffers compares multiple job offers and recommends the best choice.
func CompareOffers(ctx context.Context, offers, priorities string) (*OfferCompareResult, error) {
	offersTrunc := engine.TruncateRunes(offers, 5000, "")

	var priorityContext string
	if priorities != "" {
		priorityContext = fmt.Sprintf("CANDIDATE PRIORITIES: %s\nWeight the comparison and scoring toward these priorities.\n", priorities)
	}

	prompt := fmt.Sprintf(offerComparePrompt, offersTrunc, priorityContext)
	raw, err := engine.CallLLM(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("offer_compare LLM: %w", err)
	}

	raw = StripMarkdownFences(raw)

	var result OfferCompareResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("offer_compare parse: %w (raw: %s)", err, engine.TruncateRunes(raw, 200, "..."))
	}
	return &result, nil
}
