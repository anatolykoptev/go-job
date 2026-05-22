package jobs

import (
	"strconv"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/hunt"
)

// BountyListingToHunt converts a BountyListing (widest bounty type) to a hunt.Bounty.
// AmountCents is parsed from the human-readable Amount string (e.g. "$500" → 50000).
// Phase 2 will add full canonical URL normalisation.
func BountyListingToHunt(b engine.BountyListing) hunt.Bounty {
	amountCents := parseDollarCents(b.Amount)
	issueNum, _ := strconv.Atoi(b.IssueNum)
	h := hunt.Bounty{
		DedupHash:   hunt.DedupHash(b.URL),
		Title:       b.Title,
		URL:         b.URL,
		Org:         b.Org,
		Source:      b.Source,
		AmountCents: amountCents,
		Currency:    b.Currency,
		IssueNumber: issueNum,
		Skills:      b.Skills,
		Relevance:   b.Relevance,
	}
	return h
}

// JobListingToHunt converts a JobListing to a hunt.Job.
func JobListingToHunt(j engine.JobListing) hunt.Job {
	salMin := 0
	if j.SalaryMin != nil {
		salMin = *j.SalaryMin
	}
	salMax := 0
	if j.SalaryMax != nil {
		salMax = *j.SalaryMax
	}
	return hunt.Job{
		DedupHash:      hunt.DedupHash(j.URL),
		Title:          j.Title,
		Company:        j.Company,
		URL:            j.URL,
		Source:         j.Source,
		ExternalID:     j.JobID,
		Location:       j.Location,
		Remote:         j.Remote,
		JobType:        j.JobType,
		Experience:     j.Experience,
		SalaryMin:      salMin,
		SalaryMax:      salMax,
		SalaryCurrency: j.SalaryCurrency,
		SalaryInterval: j.SalaryInterval,
		Skills:         j.Skills,
		Description:    j.Description,
	}
}

// FreelanceProjectToHunt converts a FreelanceProject to a hunt.Freelance.
func FreelanceProjectToHunt(f engine.FreelanceProject) hunt.Freelance {
	return hunt.Freelance{
		DedupHash:   hunt.DedupHash(f.URL),
		Title:       f.Title,
		URL:         f.URL,
		Platform:    f.Platform,
		Source:      f.Platform,
		BudgetRaw:   f.Budget,
		Skills:      f.Skills,
		Description: f.Description,
		ClientInfo:  f.ClientInfo,
	}
}

// FreelanceJobToHunt converts a FreelanceJob (remoteok/himalayas type) to a hunt.Freelance.
func FreelanceJobToHunt(f engine.FreelanceJob) hunt.Freelance {
	return hunt.Freelance{
		DedupHash: hunt.DedupHash(f.URL),
		Title:     f.Title,
		URL:       f.URL,
		Platform:  f.Source,
		Source:    f.Source,
		BudgetMin: f.SalaryMin,
		BudgetMax: f.SalaryMax,
		Location:  f.Location,
		Tags:      f.Tags,
	}
}

// SecurityProgramToHunt converts a SecurityProgram to a hunt.Security.
// Min/MaxBounty are parsed from human-readable strings (e.g. "$50,000").
func SecurityProgramToHunt(s engine.SecurityProgram) hunt.Security {
	minB := parseDollarInt(s.MinBounty)
	maxB := parseDollarInt(s.MaxBounty)
	return hunt.Security{
		DedupHash:   hunt.DedupHash(s.URL),
		Name:        s.Name,
		URL:         s.URL,
		Platform:    s.Platform,
		ProgramType: s.Type,
		MinBounty:   minB,
		MaxBounty:   maxB,
		Targets:     s.Targets,
		Managed:     s.Managed,
	}
}

// parseDollarCents parses a human-readable dollar amount like "$1,500" into cents (150000).
// Returns 0 if unparseable.
func parseDollarCents(s string) int64 {
	v := parseDollarInt(s)
	return int64(v) * 100
}

// parseDollarInt parses a dollar string like "$50,000" → 50000. Returns 0 on failure.
func parseDollarInt(s string) int {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "$")
	s = strings.ReplaceAll(s, ",", "")
	// Handle ranges like "100-500": take the first number.
	if idx := strings.IndexAny(s, "-–"); idx > 0 {
		s = s[:idx]
	}
	// Strip trailing non-numeric (e.g. "50k+").
	s = strings.TrimRight(s, "k+KusdUSD ")
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return v
}
