package jobserver

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
)

// bountyDefaults returns config values with defaults applied.
func bountyHighConfidence() float32 {
	if v := engine.Cfg.BountyHighConfidence; v > 0 {
		return v
	}
	return 0.82
}
func bountyHighConfGap() float32 {
	if v := engine.Cfg.BountyHighConfGap; v > 0 {
		return v
	}
	return 0.04
}
func bountyHighConfMax() int {
	if v := engine.Cfg.BountyHighConfMax; v > 0 {
		return v
	}
	return 10
}
func bountyMedConfMax() int {
	if v := engine.Cfg.BountyMedConfMax; v > 0 {
		return v
	}
	return 3
}
func bountySkillBoost() float32 {
	if v := engine.Cfg.BountySkillBoost; v > 0 {
		return v
	}
	return 0.05
}
func bountyMinRelevance() float32 {
	if v := engine.Cfg.BountyMinRelevance; v > 0 {
		return v
	}
	return 0.75
}

const minAbsThreshold float32 = 0.3

// bountyEmbedPipeline uses precomputed bounty vectors + query embedding for matching.
func bountyEmbedPipeline(ctx context.Context, client *jobs.EmbedClient, query string, bvecs []jobs.BountyWithVector) (engine.BountySearchOutput, error) {
	// Extract bounties.
	bounties := make([]engine.BountyListing, len(bvecs))
	for i, bv := range bvecs {
		bounties[i] = bv.Bounty
	}

	if query == "" {
		// No query — sort by amount descending.
		sort.Slice(bounties, func(i, j int) bool {
			return jobs.ParseAmountCents(bounties[i].Amount) > jobs.ParseAmountCents(bounties[j].Amount)
		})
		return engine.BountySearchOutput{
			Query:    query,
			Bounties: bounties,
		}, nil
	}

	// Embed ONLY the query with e5 "query: " prefix.
	queryVec, err := client.EmbedQuery(ctx, query)
	if err != nil {
		return engine.BountySearchOutput{}, fmt.Errorf("query embedding failed: %w", err)
	}

	// Extract query keywords for skills boosting.
	querySkills := jobs.ExtractSkillsFromText(query)
	queryWords := strings.Fields(strings.ToLower(query))

	// Compute similarity against precomputed bounty vectors.
	type scored struct {
		bounty engine.BountyListing
		score  float32
	}
	var results []scored
	for i, bv := range bvecs {
		if len(bv.Vector) == 0 {
			continue
		}
		sim := jobs.CosineSimilarity(queryVec, bv.Vector)
		if sim < minAbsThreshold {
			continue
		}

		b := bounties[i]
		boost := skillsMatchBoost(querySkills, queryWords, b.Skills, b.Title)
		finalScore := sim + boost
		b.Relevance = finalScore
		results = append(results, scored{bounty: b, score: finalScore})
	}

	// Sort by score descending.
	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	// Confidence-tiered ranking.
	if len(results) > 0 {
		bestScore := results[0].score
		var maxResults int
		var cutoff float32

		if bestScore >= bountyHighConfidence() {
			maxResults = bountyHighConfMax()
			cutoff = bestScore - bountyHighConfGap()
		} else {
			maxResults = bountyMedConfMax()
			cutoff = minAbsThreshold
		}

		end := len(results)
		for i, r := range results {
			if r.score < cutoff || i >= maxResults {
				end = i
				break
			}
		}
		results = results[:end]
	}

	// If best score is below minimum relevance, return empty with guidance.
	if len(results) > 0 && results[0].score < bountyMinRelevance() {
		return engine.BountySearchOutput{
			Query:   query,
			Summary: fmt.Sprintf("No bounties closely matching %q found. Try broader keywords.", query),
		}, nil
	}

	filtered := make([]engine.BountyListing, len(results))
	for i, r := range results {
		filtered[i] = r.bounty
	}

	return engine.BountySearchOutput{
		Query:    query,
		Bounties: filtered,
	}, nil
}

// skillsMatchBoost returns a boost score if query keywords match the bounty's skills or title.
func skillsMatchBoost(querySkills, queryWords []string, bountySkills []string, bountyTitle string) float32 {
	if len(querySkills) == 0 && len(queryWords) == 0 {
		return 0
	}

	titleLower := strings.ToLower(bountyTitle)
	skillSet := make(map[string]bool, len(bountySkills))
	for _, s := range bountySkills {
		skillSet[strings.ToLower(s)] = true
	}

	for _, qs := range querySkills {
		if skillSet[strings.ToLower(qs)] {
			return bountySkillBoost()
		}
	}

	for _, w := range queryWords {
		if len(w) >= 3 && strings.Contains(titleLower, w) {
			return bountySkillBoost()
		}
	}

	return 0
}

