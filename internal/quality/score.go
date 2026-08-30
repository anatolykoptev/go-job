// Package quality provides a deterministic job-posting quality score (0-100)
// that requires no LLM and no network access. It is adapted from
// arinbalyan/scrappy's internal/quality/score.go, with the point distribution
// re-weighted for go-job's data model (hunt.Job / engine.JobListing), which
// does not carry recruiter email or company-domain fields.
//
// Score factors (100 points total, before spam penalty):
//
//	20  Salary mentioned (structured field OR parsed from description text)
//	15  Direct apply URL (URL contains "apply" or is a known direct-ATS board)
//	15  Freshness (scaled): < 24h=15, < 72h=10, < 7d=5, older=0
//	10  Description quality (length 0-10 + structured sections 0-5, capped at 10)
//	15  NOT a staffing/agency posting (company name + URL heuristics)
//	15  Source quality (scaled): direct ATS=15, major boards=10, other=5
//	10  Has a substantive description (> 100 chars)
//	---
//	100  Total
//
// Spam penalty (applied after summing positives, before clamping):
//
//	-15  Hard scam signal ("earn $N per day", "no experience needed", etc.)
//	 -5  Soft spam signal (excessive exclamation marks, ALL CAPS title, etc.)
//	     Soft signals cap at -10 total.
//
// The score is used as a cheap pre-filter before the expensive LLM fit-score
// in the hunt pipeline (issue #192) and as a standalone MCP tool.
package quality

import (
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Input is the job-posting data needed to compute a quality score.
// Construct it from hunt.Job via FromHuntJob, or from engine.JobListing via
// FromJobListing. Fields not available in the source are left zero — the
// corresponding sub-score simply contributes 0.
type Input struct {
	Title       string
	Company     string
	URL         string
	Description string
	Source      string // e.g. "greenhouse", "linkedin", "yc", "indeed"
	SalaryMin   int
	SalaryMax   int
	PostedAt    *time.Time
}

// FromHuntJob builds a quality.Input from a hunt.Job.
func FromHuntJob(j JobInput) Input {
	return Input(j)
}

// JobInput is a minimal interface satisfied by hunt.Job and any other type
// that carries the fields quality.Input needs. Using a narrow interface here
// avoids importing internal/hunt (which would create a cycle when hunt/score
// imports quality).
type JobInput struct {
	Title       string
	Company     string
	URL         string
	Description string
	Source      string
	SalaryMin   int
	SalaryMax   int
	PostedAt    *time.Time
}

// Breakdown is the per-factor point contribution returned alongside the total.
// Positive fields are >= 0; SpamPenalty is <= 0. The total is the sum of all
// fields (positives + SpamPenalty), clamped to [0, 100].
type Breakdown struct {
	Salary             int `json:"salary"`
	DirectApply        int `json:"direct_apply"`
	Freshness          int `json:"freshness"`
	DescriptionLength  int `json:"description_length"`
	StructuredSections int `json:"structured_sections"`
	NotAgency          int `json:"not_agency"`
	SourceQuality      int `json:"source_quality"`
	HasDescription     int `json:"has_description"`
	SpamPenalty        int `json:"spam_penalty"` // <= 0, negative
}

// Result is the output of Score: a 0-100 total, a per-factor breakdown, and a
// coarse verdict band used for gating and display.
//
// Verdict bands:
//
//	"high"   score >= 50
//	"medium" 25 <= score < 50
//	"low"    10 <= score < 25
//	"skip"   score < 10
type Result struct {
	Score     int       `json:"score"`
	Breakdown Breakdown `json:"breakdown"`
	Verdict   string    `json:"verdict"`
}

// Verdict constants for the Result.Verdict field.
const (
	VerdictHigh   = "high"
	VerdictMedium = "medium"
	VerdictLow    = "low"
	VerdictSkip   = "skip"
)

// Score computes a deterministic quality score (0-100) for a single job
// posting. Returns a zero Result for a nil-ish input (empty URL and empty
// description). The score is clamped to [0, 100].
func Score(in Input) Result {
	if in.URL == "" && in.Description == "" && in.Title == "" {
		return Result{Verdict: VerdictSkip}
	}

	var b Breakdown

	if hasSalary(in) {
		b.Salary = 20
	}

	if hasDirectApply(in) {
		b.DirectApply = 15
	}

	b.Freshness = freshnessScore(in.PostedAt)

	// Description quality: length (0-10) + structured sections (0-5), capped at 10.
	b.DescriptionLength = descriptionLengthScore(in.Description)
	b.StructuredSections = structuredSectionsScore(in.Description)
	descTotal := b.DescriptionLength + b.StructuredSections
	if descTotal > 10 {
		descTotal = 10
	}
	// Store the capped value back into DescriptionLength so the breakdown
	// reflects the actual contribution, and zero out StructuredSections to
	// avoid double-counting in the sum.
	b.DescriptionLength = descTotal
	b.StructuredSections = 0

	if !isAgency(in) {
		b.NotAgency = 15
	}

	b.SourceQuality = sourceQualityScore(in.Source)

	if hasSubstantiveDescription(in.Description) {
		b.HasDescription = 10
	}

	// Spam penalty (negative): applied after summing positives.
	b.SpamPenalty = spamPenalty(in)

	total := b.Salary + b.DirectApply + b.Freshness +
		b.DescriptionLength + b.NotAgency + b.SourceQuality + b.HasDescription +
		b.SpamPenalty

	if total > 100 {
		total = 100
	}
	if total < 0 {
		total = 0
	}

	return Result{
		Score:     total,
		Breakdown: b,
		Verdict:   verdictFromScore(total),
	}
}

// verdictFromScore maps a 0-100 score to a verdict band.
func verdictFromScore(score int) string {
	switch {
	case score >= 50:
		return VerdictHigh
	case score >= 25:
		return VerdictMedium
	case score >= 10:
		return VerdictLow
	default:
		return VerdictSkip
	}
}

// ---------------------------------------------------------------------------
// #231: Salary detection — structured field OR parsed from description text
// ---------------------------------------------------------------------------

// hasSalary returns true when the job lists a non-zero salary range in the
// structured fields OR when salary is detectable in the description text.
//
// Most ATS connectors (Greenhouse, Lever, Ashby, LinkedIn, Indeed, Habr)
// format salary into the description text rather than populating SalaryMin/
// SalaryMax fields. Without text scanning, the 20-point salary factor would
// be dead for ~90% of jobs (issue #231).
func hasSalary(in Input) bool {
	if in.SalaryMin > 0 || in.SalaryMax > 0 {
		return true
	}
	return salaryInText(in.Description)
}

// salaryRE matches common salary patterns in job description text:
//   - $120k, $5,000, $1.5M (USD)
//   - €80k, £60k (EUR/GBP)
//   - 200 000 руб, 200000 руб (RU)
//   - "Salary:" prefix (Lever/Ashby/LinkedIn format)
//   - 120k-180k, 120k–180k (ranges — matched by the first number)
//
// The regex is intentionally broad: false positives (e.g. "50k users") are
// acceptable because the salary factor is only 20 points and the quality gate
// threshold is 30 — a single false positive won't push a bad job past the gate.
var salaryRE = regexp.MustCompile(`(?i)(?:salary|comp(?:ensation)?|зарплата|оклад)?[:\s]*[$€£]\s?\d[\d,.]*\s?[kKmM]?|(?i)(?:salary|comp|зарплата|оклад)[:\s]*\d[\d,.]*\s?[kKmM]|(?i)\d[\d ]*\d\s*(?:руб|rub|р\.|eur|usd|грн)|(?i)\d[\d,.]*\s?[kK]\s*[-–]\s*\d[\d,.]*\s?[kK]`)

// salaryInText scans the description for salary patterns.
func salaryInText(desc string) bool {
	if len(desc) < 5 {
		return false
	}
	return salaryRE.MatchString(desc)
}

// ---------------------------------------------------------------------------
// Direct apply URL
// ---------------------------------------------------------------------------

// hasDirectApply returns true when the URL contains "apply" or the host is a
// known direct-ATS board (greenhouse, lever, ashby, workatastartup). These
// are direct-employer application endpoints, not aggregator redirect pages.
func hasDirectApply(in Input) bool {
	u := strings.ToLower(in.URL)
	if strings.Contains(u, "apply") {
		return true
	}
	// Known direct-ATS hosts.
	for _, h := range directATSHosts {
		if strings.Contains(u, h) {
			return true
		}
	}
	return false
}

// directATSHosts are host substrings that indicate a direct-employer ATS.
var directATSHosts = []string{
	"greenhouse.io",
	"lever.co",
	"ashbyhq.com",
	"workatastartup.com",
}

// ---------------------------------------------------------------------------
// Freshness
// ---------------------------------------------------------------------------

// freshnessScore returns points based on how recently the job was posted.
//
//	< 24h : 15
//	< 72h : 10
//	< 7d  :  5
//	older :  0
//
// Nil or zero PostedAt scores 0. Future dates (clock skew, timezone mismatch)
// score 0 — we cannot assert freshness for a future timestamp.
func freshnessScore(postedAt *time.Time) int {
	if postedAt == nil || postedAt.IsZero() {
		return 0
	}
	diff := time.Since(*postedAt)
	switch {
	case diff < 0:
		return 0
	case diff <= 24*time.Hour:
		return 15
	case diff <= 72*time.Hour:
		return 10
	case diff <= 7*24*time.Hour:
		return 5
	default:
		return 0
	}
}

// ---------------------------------------------------------------------------
// #232: Description quality — length + structured sections
// ---------------------------------------------------------------------------

// descriptionLengthScore returns points for description length:
//
//	> 2000 chars : 10
//	> 500  chars :  7
//	> 200  chars :  5
//	≤ 200  chars :  0
func descriptionLengthScore(desc string) int {
	n := len(strings.TrimSpace(desc))
	switch {
	case n > 2000:
		return 10
	case n > 500:
		return 7
	case n > 200:
		return 5
	default:
		return 0
	}
}

// structuredSectionsScore detects common JD section headers and awards points
// for structural clarity (issue #232). A wall of text with no sections scores
// 0; a JD with ≥3 distinct sections scores 5.
//
//	0 sections :  0
//	1 section  :  2
//	2 sections :  3
//	3+ sections :  5
//
// Section headers are detected case-insensitively as standalone lines or
// markdown headers (## Requirements, **Benefits:**, etc.).
func structuredSectionsScore(desc string) int {
	if len(desc) < 50 {
		return 0
	}
	lower := strings.ToLower(desc)
	count := 0
	for _, pat := range sectionPatterns {
		if pat.MatchString(lower) {
			count++
		}
	}
	switch {
	case count >= 3:
		return 5
	case count == 2:
		return 3
	case count == 1:
		return 2
	default:
		return 0
	}
}

// sectionPatterns are pre-compiled regexes for common JD section headers.
// Each pattern matches a single header variant as a line start (with optional
// markdown prefix like ## or **) to avoid matching body text. Each pattern
// counts independently — a JD with "What You'll Need" and "What You'll Do"
// scores 2 section hits, not 1.
var sectionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?m)^\s*(?:#{1,6}\s*|\*\*)?\s*requirements?\b`),
	regexp.MustCompile(`(?m)^\s*(?:#{1,6}\s*|\*\*)?\s*qualifications?\b`),
	regexp.MustCompile(`(?m)^\s*(?:#{1,6}\s*|\*\*)?\s*what you'?ll need\b`),
	regexp.MustCompile(`(?m)^\s*(?:#{1,6}\s*|\*\*)?\s*what you'?ll do\b`),
	regexp.MustCompile(`(?m)^\s*(?:#{1,6}\s*|\*\*)?\s*about the role\b`),
	regexp.MustCompile(`(?m)^\s*(?:#{1,6}\s*|\*\*)?\s*responsibilities?\b`),
	regexp.MustCompile(`(?m)^\s*(?:#{1,6}\s*|\*\*)?\s*what you'?ll be doing\b`),
	regexp.MustCompile(`(?m)^\s*(?:#{1,6}\s*|\*\*)?\s*benefits?\b`),
	regexp.MustCompile(`(?m)^\s*(?:#{1,6}\s*|\*\*)?\s*perks\b`),
	regexp.MustCompile(`(?m)^\s*(?:#{1,6}\s*|\*\*)?\s*what we offer\b`),
	regexp.MustCompile(`(?m)^\s*(?:#{1,6}\s*|\*\*)?\s*tech stack\b`),
	regexp.MustCompile(`(?m)^\s*(?:#{1,6}\s*|\*\*)?\s*our stack\b`),
	regexp.MustCompile(`(?m)^\s*(?:#{1,6}\s*|\*\*)?\s*technologies\b`),
	regexp.MustCompile(`(?m)^\s*(?:#{1,6}\s*|\*\*)?\s*about us\b`),
	regexp.MustCompile(`(?m)^\s*(?:#{1,6}\s*|\*\*)?\s*about the team\b`),
	regexp.MustCompile(`(?m)^\s*(?:#{1,6}\s*|\*\*)?\s*who we are\b`),
	regexp.MustCompile(`(?m)^\s*(?:#{1,6}\s*|\*\*)?\s*nice.to.have\b`),
	regexp.MustCompile(`(?m)^\s*(?:#{1,6}\s*|\*\*)?\s*preferred qualifications?\b`),
	regexp.MustCompile(`(?m)^\s*(?:#{1,6}\s*|\*\*)?\s*bonus\b`),
}

// hasSubstantiveDescription returns true when the description is non-empty and
// longer than 100 characters — a stub vs. a real JD.
func hasSubstantiveDescription(desc string) bool {
	return len(strings.TrimSpace(desc)) > 100
}

// ---------------------------------------------------------------------------
// Source quality
// ---------------------------------------------------------------------------

// sourceQualityScore returns points based on the job source:
//
//	direct ATS (greenhouse, lever, ashby, yc) : 15
//	major boards (linkedin, indeed)           : 10
//	other                                      : 5
func sourceQualityScore(source string) int {
	s := strings.ToLower(strings.TrimSpace(source))
	for _, ats := range directATSSources {
		if s == ats {
			return 15
		}
	}
	for _, board := range majorBoardSources {
		if s == board {
			return 10
		}
	}
	if s == "" {
		return 0
	}
	return 5
}

var directATSSources = []string{
	"greenhouse", "lever", "ashby", "yc", "workatastartup",
}

var majorBoardSources = []string{
	"linkedin", "indeed",
}

// ---------------------------------------------------------------------------
// Agency detection
// ---------------------------------------------------------------------------

// agencyDomains lists known staffing/agency company domains.
var agencyDomains = []string{
	"aerotek.com",
	"adecco.com",
	"ciber.com",
	"collateraledge.com",
	"experis.com",
	"hays.com",
	"insightglobal.com",
	"kellyservices.com",
	"kforce.com",
	"manpower.com",
	"michaelpage.com",
	"modis.com",
	"randstad.com",
	"roberthalf.com",
	"robertwalters.com",
	"spencerogden.com",
	"teksystems.com",
}

// agencyTokens are standalone-word tokens in a company name that signal a
// staffing / recruiting agency rather than a direct employer.
var agencyTokens = []string{
	"staffing",
	"recruiting",
	"recruitment",
	"agency",
	"talent",
	"workforce",
	"placement",
}

// isAgency returns true when the job's URL host or company name suggests a
// staffing / recruiting agency rather than a direct employer.
func isAgency(in Input) bool {
	u := strings.ToLower(in.URL)
	if u != "" {
		for _, d := range agencyDomains {
			if strings.Contains(u, d) {
				return true
			}
		}
	}

	name := strings.ToLower(strings.TrimSpace(in.Company))
	if name == "" {
		return false
	}
	for _, token := range agencyTokens {
		if hasToken(name, token) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// #233: Spam/scam marker detection
// ---------------------------------------------------------------------------

// spamPenalty returns a negative penalty when spam/scam markers are detected
// in the title or description (issue #233).
//
//	-15  Hard scam signal (any one triggers the full penalty)
//	 -5  Soft spam signal (each, capped at -10 total)
//
// Hard and soft penalties stack: a posting with one hard + two soft signals
// gets -25.
func spamPenalty(in Input) int {
	penalty := 0

	// Hard scam signals: -15 for any match.
	if hasHardSpamSignal(in) {
		penalty -= 15
	}

	// Soft spam signals: -5 each, capped at -10 total.
	softCount := countSoftSpamSignals(in)
	if softCount > 0 {
		softPenalty := softCount * 5
		if softPenalty > 10 {
			softPenalty = 10
		}
		penalty -= softPenalty
	}

	return penalty
}

// hardSpamREs are regexes for classic job-scam patterns. Any single match
// triggers the full -15 penalty.
var hardSpamREs = []*regexp.Regexp{
	// "earn $5000 per day/week" — get-rich-quick.
	regexp.MustCompile(`(?i)earn\s*\$?\s*\d[\d,]*\s*(?:per|/)\s*(?:day|week|hour)`),
	// "no experience needed/required" — classic scam qualifier.
	regexp.MustCompile(`(?i)no\s+experience\s+(?:needed|required|necessary)`),
	// "be your own boss" — MLM/pyramid scheme language.
	regexp.MustCompile(`(?i)be\s+your\s+own\s+boss`),
	// "work from home" + "earn" + dollar amount — WFH scam pattern.
	regexp.MustCompile(`(?i)work\s+from\s+home[^.]{0,100}earn\s*\$?\s*\d`),
	// "immediate start" + "no experience" — scam combo.
	regexp.MustCompile(`(?i)immediate\s+start[^.]{0,80}no\s+experience`),
}

// hasHardSpamSignal checks if any hard scam pattern matches.
func hasHardSpamSignal(in Input) bool {
	text := in.Title + "\n" + in.Description
	for _, re := range hardSpamREs {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// countSoftSpamSignals counts how many soft spam markers are present.
// Each distinct signal contributes -5, capped at -10 by the caller.
func countSoftSpamSignals(in Input) int {
	count := 0

	// 1. Excessive exclamation marks (>5 in description).
	if strings.Count(in.Description, "!") > 5 {
		count++
	}

	// 2. ALL CAPS title (>50% uppercase letters, min 10 chars).
	if isAllCapSTitle(in.Title) {
		count++
	}

	// 3. "urgent hiring" / "immediate hire" — pressure tactics.
	lower := strings.ToLower(in.Title + " " + in.Description)
	if strings.Contains(lower, "urgent hiring") || strings.Contains(lower, "immediate hire") {
		count++
	}

	// 4. "click here to apply" — phishing redirect pattern.
	if strings.Contains(lower, "click here to apply") {
		count++
	}

	return count
}

// isAllCapSTitle returns true when >50% of letters in the title are uppercase
// and the title has at least 10 characters — a common spam marker.
func isAllCapSTitle(title string) bool {
	title = strings.TrimSpace(title)
	if len(title) < 10 {
		return false
	}
	upper, total := 0, 0
	for _, r := range title {
		if unicode.IsLetter(r) {
			total++
			if unicode.IsUpper(r) {
				upper++
			}
		}
	}
	if total == 0 {
		return false
	}
	return float64(upper)/float64(total) > 0.5
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// hasToken reports whether token appears as a standalone word in s.
func hasToken(s, token string) bool {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	for _, f := range fields {
		if f == token {
			return true
		}
	}
	return false
}
