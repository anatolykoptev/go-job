package jobs

import (
	"fmt"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
)

func bountyToOpportunity(b engine.BountyListing) engine.Opportunity {
	return engine.Opportunity{
		Type:   "bounty",
		Title:  b.Title,
		URL:    b.URL,
		Reward: b.Amount,
		Source: b.Source,
		Skills: b.Skills,
		Posted: b.Posted,
	}
}

func securityToOpportunity(s engine.SecurityProgram) engine.Opportunity {
	reward := s.MaxBounty
	if s.MinBounty != "" && s.MaxBounty != "" {
		reward = s.MinBounty + " - " + s.MaxBounty
	}

	summary := ""
	if len(s.Targets) > 0 {
		summary = strings.Join(s.Targets, ", ")

		const maxSummaryLen = 200
		if len(summary) > maxSummaryLen {
			summary = summary[:maxSummaryLen] + "..."
		}
	}

	return engine.Opportunity{
		Type:    "security",
		Title:   s.Name,
		URL:     s.URL,
		Reward:  reward,
		Source:  s.Platform,
		Skills:  nil,
		Summary: summary,
	}
}

func freelanceToOpportunity(f engine.FreelanceJob) engine.Opportunity {
	title := f.Title
	if f.Company != "" {
		title = f.Title + " @ " + f.Company
	}

	reward := ""
	if f.SalaryMin > 0 && f.SalaryMax > 0 {
		reward = fmt.Sprintf("$%d-$%d", f.SalaryMin, f.SalaryMax)
	} else if f.SalaryMax > 0 {
		reward = fmt.Sprintf("Up to $%d", f.SalaryMax)
	}

	return engine.Opportunity{
		Type:   "freelance",
		Title:  title,
		URL:    f.URL,
		Reward: reward,
		Source: f.Source,
		Skills: f.Tags,
		Posted: f.Posted,
	}
}

// skillAliases maps common search terms to their canonical forms and vice versa.
// When a user searches for "golang", we also match "go"; when they search "js", we match "javascript".
var skillAliases = map[string][]string{
	"golang":     {"go"},
	"go":         {"golang"},
	"javascript": {"js", "node", "nodejs"},
	"js":         {"javascript"},
	"typescript": {"ts"},
	"ts":         {"typescript"},
	"python":     {"py"},
	"py":         {"python"},
	"rust":       {"rs"},
	"csharp":     {"c#", ".net"},
	"c#":         {"csharp", ".net"},
	"cpp":        {"c++"},
	"c++":        {"cpp"},
}

func filterOpportunities(opps []engine.Opportunity, query string) []engine.Opportunity {
	queries := expandQueryAliases(query)
	var filtered []engine.Opportunity

	for _, o := range opps {
		if matchesAny(o, queries) {
			filtered = append(filtered, o)
		}
	}

	return filtered
}

func expandQueryAliases(query string) []string {
	queries := []string{query}
	if aliases, ok := skillAliases[query]; ok {
		queries = append(queries, aliases...)
	}
	return queries
}

func matchesAny(o engine.Opportunity, queries []string) bool {
	titleLower := strings.ToLower(o.Title)
	sourceLower := strings.ToLower(o.Source)
	summaryLower := strings.ToLower(o.Summary)

	for _, q := range queries {
		if strings.Contains(titleLower, q) ||
			strings.Contains(sourceLower, q) ||
			strings.Contains(summaryLower, q) ||
			skillsContain(o.Skills, q) {
			return true
		}
	}
	return false
}

func skillsContain(skills []string, query string) bool {
	for _, s := range skills {
		if strings.EqualFold(s, query) || strings.Contains(strings.ToLower(s), query) {
			return true
		}
	}

	return false
}
