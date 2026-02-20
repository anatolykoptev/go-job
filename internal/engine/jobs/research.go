package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// --- Salary Research ---

// SalaryResearchResult is the structured output of salary_research.
type SalaryResearchResult struct {
	Role       string   `json:"role"`
	Location   string   `json:"location"`
	Currency   string   `json:"currency"`
	P25        int      `json:"p25"`
	Median     int      `json:"median"`
	P75        int      `json:"p75"`
	Sources    []string `json:"sources"`
	Notes      string   `json:"notes"`
	UpdatedAt  string   `json:"updated_at"`
}

const salaryResearchPrompt = `You are a compensation research expert. Based on the search results below, provide salary data for the role.

Role: %s
Location: %s
Experience: %s

Search results:
%s

Return a JSON object with this exact structure:
{
  "role": "<normalized role title>",
  "location": "<location>",
  "currency": "<currency code, e.g. USD, EUR, RUB>",
  "p25": <25th percentile salary as integer>,
  "median": <median salary as integer>,
  "p75": <75th percentile salary as integer>,
  "sources": [<list of sources mentioned in search results>],
  "notes": "<any important caveats, e.g. equity not included, varies by company size>",
  "updated_at": "<approximate date of data, e.g. 2025>"
}

Use annual salary figures. If location is Russia/RU, use RUB. Otherwise use USD by default.
Return ONLY the JSON object, no markdown, no explanation.`

// ResearchSalary aggregates salary data for a role+location via SearXNG + LLM synthesis.
func ResearchSalary(ctx context.Context, role, location, experience string) (*SalaryResearchResult, error) {
	queries := buildSalaryQueries(role, location, experience)

	type searchRes struct {
		results []engine.SearxngResult
		err     error
	}
	ch := make(chan searchRes, len(queries))
	for _, q := range queries {
		go func(query string) {
			r, err := engine.SearchSearXNG(ctx, query, "all", "", "google")
			ch <- searchRes{r, err}
		}(q)
	}

	var allSnippets []string
	for range queries {
		res := <-ch
		if res.err != nil {
			continue
		}
		for _, r := range res.results {
			if r.Content != "" {
				allSnippets = append(allSnippets, fmt.Sprintf("**%s**\n%s\n%s", r.Title, r.URL, engine.TruncateRunes(r.Content, 300, "...")))
			}
		}
	}

	if len(allSnippets) == 0 {
		return nil, fmt.Errorf("salary_research: no search results found for %q in %q", role, location)
	}

	searchText := strings.Join(allSnippets, "\n\n---\n\n")
	searchText = engine.TruncateRunes(searchText, 6000, "")

	prompt := fmt.Sprintf(salaryResearchPrompt, role, location, experience, searchText)
	raw, err := engine.CallLLM(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("salary_research LLM: %w", err)
	}

	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var result SalaryResearchResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("salary_research parse: %w (raw: %s)", err, engine.TruncateRunes(raw, 200, "..."))
	}
	return &result, nil
}

// buildSalaryQueries constructs search queries for salary data from multiple sources.
func buildSalaryQueries(role, location, experience string) []string {
	base := role
	if experience != "" {
		base = experience + " " + role
	}

	queries := []string{
		fmt.Sprintf("%s salary %s 2025 site:levels.fyi OR site:glassdoor.com OR site:linkedin.com", base, location),
		fmt.Sprintf("%s зарплата %s 2025 site:hh.ru OR site:habr.com OR site:career.habr.com", base, location),
		fmt.Sprintf("%s salary range %s average annual compensation", base, location),
	}

	// For Russian locations, add RU-specific sources
	if isRussianLocation(location) {
		queries = []string{
			fmt.Sprintf("%s зарплата %s 2025 site:hh.ru", base, location),
			fmt.Sprintf("%s зарплата %s site:career.habr.com OR site:zarplata.ru", base, location),
			fmt.Sprintf("%s salary %s 2025", base, location),
		}
	}

	return queries
}

// isRussianLocation returns true if the location appears to be in Russia/CIS.
func isRussianLocation(location string) bool {
	loc := strings.ToLower(location)
	ruKeywords := []string{"россия", "москва", "санкт-петербург", "спб", "russia", "moscow", "saint-petersburg", "ru"}
	for _, kw := range ruKeywords {
		if strings.Contains(loc, kw) {
			return true
		}
	}
	return false
}

// --- Company Research ---

// CompanyResearchResult is the structured output of company_research.
type CompanyResearchResult struct {
	Name        string   `json:"name"`
	Size        string   `json:"size"`
	Founded     string   `json:"founded"`
	Industry    string   `json:"industry"`
	Funding     string   `json:"funding"`
	TechStack   []string `json:"tech_stack"`
	CultureNotes string  `json:"culture_notes"`
	RecentNews  []string `json:"recent_news"`
	GlassdoorRating float64 `json:"glassdoor_rating"`
	Website     string   `json:"website"`
	Summary     string   `json:"summary"`
}

const companyResearchPrompt = `You are a company research analyst. Based on the search results below, provide a comprehensive company overview.

Company: %s

Search results:
%s

Return a JSON object with this exact structure:
{
  "name": "<official company name>",
  "size": "<employee count or range, e.g. '1000-5000', '50-200', 'startup <50'>",
  "founded": "<founding year or decade>",
  "industry": "<primary industry>",
  "funding": "<funding stage and amount if known, e.g. 'Series B, $50M' or 'Public (NASDAQ: XYZ)'>",
  "tech_stack": [<technologies the company uses, from job postings or engineering blog>],
  "culture_notes": "<2-3 sentences about work culture, values, remote policy>",
  "recent_news": [<up to 3 recent notable events: funding, product launches, layoffs, etc.>],
  "glassdoor_rating": <rating as float 0-5, or 0 if unknown>,
  "website": "<company website URL>",
  "summary": "<3-4 sentence overall company overview for a job seeker>"
}

Return ONLY the JSON object, no markdown, no explanation.`

// ResearchCompany fetches company overview from multiple sources via SearXNG + LLM.
func ResearchCompany(ctx context.Context, companyName string) (*CompanyResearchResult, error) {
	queries := []string{
		fmt.Sprintf("%s company overview employees funding tech stack", companyName),
		fmt.Sprintf("%s reviews culture glassdoor work life balance", companyName),
		fmt.Sprintf("%s news 2024 2025 site:techcrunch.com OR site:crunchbase.com OR site:linkedin.com", companyName),
	}

	type searchRes struct {
		results []engine.SearxngResult
		err     error
	}
	ch := make(chan searchRes, len(queries))
	for _, q := range queries {
		go func(query string) {
			r, err := engine.SearchSearXNG(ctx, query, "all", "", "google")
			ch <- searchRes{r, err}
		}(q)
	}

	var allSnippets []string
	for range queries {
		res := <-ch
		if res.err != nil {
			continue
		}
		for _, r := range res.results {
			if r.Content != "" {
				allSnippets = append(allSnippets, fmt.Sprintf("**%s**\n%s\n%s", r.Title, r.URL, engine.TruncateRunes(r.Content, 400, "...")))
			}
		}
	}

	if len(allSnippets) == 0 {
		return nil, fmt.Errorf("company_research: no results found for %q", companyName)
	}

	searchText := strings.Join(allSnippets, "\n\n---\n\n")
	searchText = engine.TruncateRunes(searchText, 7000, "")

	prompt := fmt.Sprintf(companyResearchPrompt, companyName, searchText)
	raw, err := engine.CallLLM(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("company_research LLM: %w", err)
	}

	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var result CompanyResearchResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("company_research parse: %w (raw: %s)", err, engine.TruncateRunes(raw, 200, "..."))
	}
	return &result, nil
}
