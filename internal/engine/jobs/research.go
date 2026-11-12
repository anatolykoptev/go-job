package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// DefaultCompanyResearchTimeout bounds the OPTIONAL company-research substep on
// enrichment paths (resume_generate, application_prep). The substep fans out
// web searches plus an LLM synthesis call; when the search backend is slow it
// can dominate the parent tool's runtime and previously pushed resume_generate
// past the 90s ToolTimeout (observed: a 1m30s timeout vs a 35s success on the
// same input). Because the company context only *enriches* the prompt — it is
// never required to produce a resume — the substep is bounded here so a slow
// research degrades to "no company context" instead of failing the whole tool.
const DefaultCompanyResearchTimeout = 25 * time.Second

// maxConcurrentSearches caps the number of parallel searchWeb goroutines
// in fanOutSearchWeb. Prevents goroutine explosion when a caller passes many
// queries; 3 is enough to keep latency low without overwhelming the search
// backend.
const maxConcurrentSearches = 3

// searchWebFn and callResearchLLM are package-level seams for deterministic unit testing.
var (
	searchWebFn     = searchWeb
	callResearchLLM = engine.CallLLM
)

// fanOutSearchWeb runs searchWeb for each query in parallel, bounded by
// maxConcurrentSearches. Returns the merged results from all queries.
// The channel is always fully drained (buffered, blocking send) so no
// goroutine leaks even on context cancellation.
func fanOutSearchWeb(ctx context.Context, queries []string) [][]engine.SearxngResult {
	if len(queries) == 0 {
		return nil
	}
	sem := make(chan struct{}, maxConcurrentSearches)
	ch := make(chan []engine.SearxngResult, len(queries))
	for _, q := range queries {
		go func(query string) {
			sem <- struct{}{}
			defer func() { <-sem }()
			ch <- searchWebFn(ctx, query)
		}(q)
	}
	out := make([][]engine.SearxngResult, len(queries))
	for i := range queries {
		out[i] = <-ch
	}
	return out
}

// --- Salary Research ---

// SalaryResearchResult is the structured output of salary_research.
type SalaryResearchResult struct {
	Role      string   `json:"role"`
	Location  string   `json:"location"`
	Currency  string   `json:"currency"`
	P25       int      `json:"p25"`
	Median    int      `json:"median"`
	P75       int      `json:"p75"`
	Sources   []string `json:"sources"`
	Notes     string   `json:"notes"`
	UpdatedAt string   `json:"updated_at"`
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

// ResearchSalary aggregates salary data for a role+location via go-search/DIRECT + LLM synthesis.
func ResearchSalary(ctx context.Context, role, location, experience string) (*SalaryResearchResult, error) {
	queries := buildSalaryQueries(role, location, experience)

	results := fanOutSearchWeb(ctx, queries)

	var allSnippets []string
	for _, res := range results {
		for _, r := range res {
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
	raw, err := callResearchLLM(ctx, prompt)
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
	Name            string   `json:"name"`
	Matched         bool     `json:"matched"`
	Confidence      string   `json:"confidence,omitempty"`
	Size            string   `json:"size"`
	Founded         string   `json:"founded"`
	Industry        string   `json:"industry"`
	Funding         string   `json:"funding"`
	TechStack       []string `json:"tech_stack"`
	CultureNotes    string   `json:"culture_notes"`
	RecentNews      []string `json:"recent_news"`
	GlassdoorRating float64  `json:"glassdoor_rating"`
	Website         string   `json:"website"`
	Sources         []string `json:"sources,omitempty"`
	Summary         string   `json:"summary"`
}

const companyResearchPrompt = `You are a company research analyst. Based ONLY on the search results below, extract verified information about the target company.

Company: %s

Search results:
%s

Rules:
1. Use ONLY facts explicitly stated in the search results that clearly refer to "%s".
2. Do NOT guess, fabricate, or extrapolate missing fields. Do NOT assume the company is in the IT or tech sector unless explicitly stated in the search results.
3. Never output template or placeholder values (such as "NASDAQ: XYZ", "NYSE: XYZ", "Series B, $50M", "50-200", "2010s", "unknown", or "N/A").
4. If a field is not explicitly supported by the search results, set strings to "", arrays to [], and glassdoor_rating to 0.
5. If the search results do not contain enough reliable information about this specific company, set "matched": false, "confidence": "none", leave all factual fields empty, and set "summary" to "Insufficient data: no verified company information found in search results."

Return a JSON object with this exact structure:
{
  "name": "",
  "matched": true,
  "confidence": "high",
  "size": "",
  "founded": "",
  "industry": "",
  "funding": "",
  "tech_stack": [],
  "culture_notes": "",
  "recent_news": [],
  "glassdoor_rating": 0,
  "website": "",
  "summary": ""
}

Valid values for "confidence": "high", "medium", "low", "none".
Return ONLY the JSON object, no markdown, no explanation.`

// insufficientCompanyResearchResult returns a clean, non-hallucinated response
// when search snippets do not contain verified data for companyName.
func insufficientCompanyResearchResult(companyName string) *CompanyResearchResult {
	return &CompanyResearchResult{
		Name:       companyName,
		Matched:    false,
		Confidence: "none",
		TechStack:  []string{},
		RecentNews: []string{},
		Summary:    fmt.Sprintf("Insufficient data: no verified information found for %q in search results.", companyName),
	}
}

// companySearchTokens extracts normalized significant tokens from a company name.
func companySearchTokens(companyName string) []string {
	stopWords := map[string]bool{
		"inc": true, "llc": true, "ltd": true, "corp": true, "corporation": true,
		"company": true, "co": true, "gmbh": true, "ag": true, "plc": true,
		"ооо": true, "ао": true, "зао": true, "пао": true, "the": true,
	}
	cleaned := strings.ToLower(strings.TrimSpace(companyName))
	replacer := strings.NewReplacer(",", " ", ".", " ", "-", " ", "_", " ", "(", " ", ")", " ", "\"", " ", "'", " ")
	cleaned = replacer.Replace(cleaned)
	var tokens []string
	for _, part := range strings.Fields(cleaned) {
		if stopWords[part] {
			continue
		}
		if len([]rune(part)) >= 2 {
			tokens = append(tokens, part)
		}
	}
	return tokens
}

// filterRelevantCompanyResults filters raw search results to those whose title,
// URL, or content actually mentions the target company name or its significant tokens.
// Also returns the total count of non-empty raw snippets so the caller can distinguish
// "search backend returned nothing" (error) from "search returned unrelated pages" (insufficient data).
func filterRelevantCompanyResults(companyName string, results [][]engine.SearxngResult) (matched []engine.SearxngResult, sources []string, totalRaw int) {
	normCompany := strings.ToLower(strings.TrimSpace(companyName))
	tokens := companySearchTokens(companyName)
	seenURL := make(map[string]bool)

	for _, group := range results {
		for _, r := range group {
			if strings.TrimSpace(r.Content) == "" {
				continue
			}
			totalRaw++
			haystack := strings.ToLower(r.Title + " " + r.URL + " " + r.Content)
			isMatch := normCompany != "" && strings.Contains(haystack, normCompany)
			if !isMatch {
				for _, tok := range tokens {
					if len([]rune(tok)) >= 3 && strings.Contains(haystack, tok) {
						isMatch = true
						break
					}
				}
			}
			if !isMatch {
				continue
			}
			matched = append(matched, r)
			if u := strings.TrimSpace(r.URL); u != "" && !seenURL[u] {
				seenURL[u] = true
				sources = append(sources, u)
			}
		}
	}
	return matched, sources, totalRaw
}

// isPlaceholderCompanyValue detects template placeholders and generic non-answers
// (such as "Public (NASDAQ: XYZ)" or "<primary industry>") in LLM output.
func isPlaceholderCompanyValue(val string) bool {
	v := strings.TrimSpace(val)
	if v == "" {
		return true
	}
	lower := strings.ToLower(v)
	exactPlaceholders := map[string]bool{
		"unknown": true, "n/a": true, "na": true, "none": true,
		"not available": true, "not disclosed": true, "undisclosed": true,
		"tbd": true, "null": true, "unspecified": true,
	}
	if exactPlaceholders[lower] {
		return true
	}
	substringPlaceholders := []string{
		"nasdaq: xyz",
		"nyse: xyz",
		"series b, $50m",
		"<official company name>",
		"<employee count",
		"<founding year",
		"<primary industry>",
		"<funding stage",
		"<company website",
	}
	for _, sub := range substringPlaceholders {
		if strings.Contains(lower, sub) {
			return true
		}
	}
	if strings.HasPrefix(v, "<") && strings.HasSuffix(v, ">") {
		return true
	}
	return false
}

func cleanCompanyField(val string) string {
	if isPlaceholderCompanyValue(val) {
		return ""
	}
	return strings.TrimSpace(val)
}

func cleanCompanySlice(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if cleaned := cleanCompanyField(item); cleaned != "" {
			out = append(out, cleaned)
		}
	}
	return out
}

func hasVerifiedCompanyData(res *CompanyResearchResult) bool {
	if res == nil {
		return false
	}
	return res.Industry != "" ||
		res.Website != "" ||
		res.Size != "" ||
		res.Founded != "" ||
		res.Funding != "" ||
		res.CultureNotes != "" ||
		len(res.TechStack) > 0 ||
		len(res.RecentNews) > 0
}

// sanitizeCompanyResearchResult strips placeholder artifacts and enforces the
// insufficient-data contract when the LLM reports low/no confidence or when
// all extracted fields were placeholders.
func sanitizeCompanyResearchResult(companyName string, res *CompanyResearchResult, sources []string) {
	if res == nil {
		return
	}
	res.Name = cleanCompanyField(res.Name)
	if res.Name == "" {
		res.Name = companyName
	}
	res.Size = cleanCompanyField(res.Size)
	res.Founded = cleanCompanyField(res.Founded)
	res.Industry = cleanCompanyField(res.Industry)
	res.Funding = cleanCompanyField(res.Funding)
	res.CultureNotes = cleanCompanyField(res.CultureNotes)
	res.Website = cleanCompanyField(res.Website)
	res.Summary = cleanCompanyField(res.Summary)
	res.TechStack = cleanCompanySlice(res.TechStack)
	res.RecentNews = cleanCompanySlice(res.RecentNews)

	conf := strings.ToLower(strings.TrimSpace(res.Confidence))
	switch conf {
	case "high", "medium", "low", "none":
		res.Confidence = conf
	default:
		if hasVerifiedCompanyData(res) {
			res.Confidence = "medium"
		} else {
			res.Confidence = "none"
		}
	}

	summaryLower := strings.ToLower(res.Summary)
	summaryIndicatesInsufficient := strings.Contains(summaryLower, "insufficient data") ||
		strings.Contains(summaryLower, "no verified") ||
		strings.Contains(summaryLower, "not enough information")

	if res.Confidence == "none" || summaryIndicatesInsufficient || !hasVerifiedCompanyData(res) {
		res.Matched = false
		res.Confidence = "none"
		res.Size = ""
		res.Founded = ""
		res.Industry = ""
		res.Funding = ""
		res.TechStack = []string{}
		res.CultureNotes = ""
		res.RecentNews = []string{}
		res.GlassdoorRating = 0
		res.Website = ""
		res.Sources = nil
		if !strings.Contains(strings.ToLower(res.Summary), "insufficient data") {
			res.Summary = fmt.Sprintf("Insufficient data: no verified information found for %q in search results.", companyName)
		}
		return
	}

	res.Matched = true
	if len(sources) > 0 && len(res.Sources) == 0 {
		res.Sources = sources
	}
}

// ResearchCompany fetches company overview from multiple sources via go-engine DIRECT
// (DDG, Wikipedia, Marginalia, etc.) with SearXNG as an optional additive source
// when SEARXNG_URL is configured. Primary path uses SearchDirect which is non-fatal
// on per-source failures and always returns usable results when any scraper responds.
func ResearchCompany(ctx context.Context, companyName string) (*CompanyResearchResult, error) {
	queries := []string{
		fmt.Sprintf("%q company overview website industry", companyName),
		fmt.Sprintf("%q employees founded culture reviews", companyName),
		fmt.Sprintf("%q news funding site:techcrunch.com OR site:crunchbase.com OR site:linkedin.com", companyName),
	}

	// Fan out all queries in parallel via searchWeb (go-search primary +
	// SearchDirect fallback), bounded by maxConcurrentSearches.
	results := fanOutSearchWeb(ctx, queries)

	matchedResults, sources, totalRaw := filterRelevantCompanyResults(companyName, results)
	if totalRaw == 0 {
		return nil, fmt.Errorf("company_research: no results found for %q", companyName)
	}
	if len(matchedResults) == 0 {
		return insufficientCompanyResearchResult(companyName), nil
	}

	var allSnippets []string
	for _, r := range matchedResults {
		allSnippets = append(allSnippets, fmt.Sprintf("**%s**\n%s\n%s", r.Title, r.URL, engine.TruncateRunes(r.Content, 400, "...")))
	}

	searchText := strings.Join(allSnippets, "\n\n---\n\n")
	searchText = engine.TruncateRunes(searchText, 7000, "")

	prompt := fmt.Sprintf(companyResearchPrompt, companyName, searchText, companyName)
	raw, err := callResearchLLM(ctx, prompt)
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
	sanitizeCompanyResearchResult(companyName, &result, sources)
	return &result, nil
}

// ResearchCompanyBounded runs ResearchCompany under a hard sub-deadline so a
// slow/hung company-research substep can never block a parent tool past its
// budget. It is the single authority for the optional-enrichment degradation
// policy — every optional caller (resume_generate, application_prep) goes
// through here rather than calling ResearchCompany directly with the raw
// request context.
//
// On timeout or any error it returns (nil, nil): callers treat a nil result as
// "no company context" and proceed. The outcome is recorded on
// gojob_company_research_total{outcome} so the previously-silent slow path is
// observable. A timeout==0 disables the bound (falls back to the parent ctx).
func ResearchCompanyBounded(ctx context.Context, companyName string, timeout time.Duration) *CompanyResearchResult {
	if companyName == "" {
		return nil
	}
	if timeout <= 0 {
		timeout = DefaultCompanyResearchTimeout
	}

	subCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type res struct {
		cr  *CompanyResearchResult
		err error
	}
	// Buffered so the worker never blocks on send if we returned on timeout.
	ch := make(chan res, 1)
	go func() {
		cr, err := ResearchCompany(subCtx, companyName)
		ch <- res{cr, err}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			// Classify by the sub-deadline state, not by r.err: ResearchCompany
			// swallows a dead context into a plain "no results found" error (its
			// parallel searches all fail and `continue`), so r.err is usually NOT
			// errors.Is(DeadlineExceeded) even when the bound is what killed it.
			// subCtx.Err() is the authoritative "did the bound fire" signal — this
			// keeps the timeout vs error label deterministic instead of letting a
			// goroutine race split real timeouts across both labels.
			if errors.Is(subCtx.Err(), context.DeadlineExceeded) {
				engine.IncrCompanyResearch("timeout")
				slog.Warn("company research timed out; proceeding without company context",
					slog.String("company", companyName), slog.Duration("timeout", timeout))
			} else {
				engine.IncrCompanyResearch("error")
				slog.Warn("company research failed; proceeding without company context",
					slog.String("company", companyName), slog.Any("error", r.err))
			}
			return nil
		}
		engine.IncrCompanyResearch("ok")
		return r.cr
	case <-subCtx.Done():
		engine.IncrCompanyResearch("timeout")
		slog.Warn("company research exceeded sub-deadline; proceeding without company context",
			slog.String("company", companyName), slog.Duration("timeout", timeout))
		return nil
	}
}

// BuildCompanyContext formats CompanyResearchResult into a prompt-ready context block.
// Returns empty string if res is nil, unverified, or has no useful data.
func BuildCompanyContext(company string, res *CompanyResearchResult) string {
	if res == nil || (!res.Matched && res.Confidence == "none") {
		return ""
	}
	var parts []string
	if len(res.TechStack) > 0 {
		parts = append(parts, "Tech stack: "+strings.Join(res.TechStack, ", "))
	}
	if res.CultureNotes != "" {
		parts = append(parts, "Culture: "+res.CultureNotes)
	}
	if len(res.RecentNews) > 0 {
		parts = append(parts, "Recent news: "+strings.Join(res.RecentNews, "; "))
	}
	if res.Size != "" {
		parts = append(parts, "Size: "+res.Size)
	}
	if res.Industry != "" {
		parts = append(parts, "Industry: "+res.Industry)
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf("\nCOMPANY CONTEXT (%s):\n%s\n", company, strings.Join(parts, "\n"))
}
