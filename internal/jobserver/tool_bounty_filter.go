package jobserver

import (
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
)

// filterBountyInputs applies min_amount and skills filters to bounties.
func filterBountyInputs(bvecs []jobs.BountyWithVector, input engine.BountySearchInput) []jobs.BountyWithVector {
	if input.MinAmount == 0 && len(input.Skills) == 0 {
		return bvecs
	}

	var filtered []jobs.BountyWithVector
	for _, bv := range bvecs {
		if input.MinAmount > 0 {
			cents := jobs.ParseAmountCents(bv.Bounty.Amount)
			if cents < input.MinAmount*100 {
				continue
			}
		}
		if len(input.Skills) > 0 {
			if !hasMatchingSkill(bv.Bounty.Skills, input.Skills) {
				continue
			}
		}
		filtered = append(filtered, bv)
	}
	return filtered
}

func hasMatchingSkill(bountySkills, querySkills []string) bool {
	set := make(map[string]bool, len(bountySkills))
	for _, s := range bountySkills {
		set[strings.ToLower(s)] = true
	}
	for _, s := range querySkills {
		if set[strings.ToLower(s)] {
			return true
		}
	}
	return false
}
