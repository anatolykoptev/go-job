// Package quality provides a deterministic job-posting quality score (0-100)
// that requires no LLM and no network access. It is adapted from
// arinbalyan/scrappy's internal/quality/score.go, with the point distribution
// re-weighted for go-job's data model (hunt.Job / engine.JobListing), which
// does not carry recruiter email or company-domain fields.
//
// Score factors (100 points total):
//
//	20  Salary mentioned (non-zero min or max)
//	15  Direct apply URL (URL contains "apply" or is a known direct-ATS board)
//	15  Freshness (scaled): < 24h=15, < 72h=10, < 7d=5, older=0
//	10  Description length (scaled): > 2000=10, > 500=7, > 200=5
//	15  NOT a staffing/agency posting (company name + URL heuristics)
//	15  Source quality (scaled): direct ATS=15, major boards=10, other=5
//	10  Has a substantive description (> 100 chars)
//	---
//	100  Total
//
// The score is used as a cheap pre-filter before the expensive LLM fit-score
// in the hunt pipeline (issue #192) and as a standalone MCP tool.
package quality

import (
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
// All fields are >= 0 except where noted; the total is the sum of all fields.
type Breakdown struct {
	Salary            int `json:"salary"`
	DirectApply       int `json:"direct_apply"`
	Freshness         int `json:"freshness"`
	DescriptionLength int `json:"description_length"`
	NotAgency         int `json:"not_agency"`
	SourceQuality     int `json:"source_quality"`
	HasDescription    int `json:"has_description"`
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

	b.DescriptionLength = descriptionLengthScore(in.Description)

	if !isAgency(in) {
		b.NotAgency = 15
	}

	b.SourceQuality = sourceQualityScore(in.Source)

	if hasSubstantiveDescription(in.Description) {
		b.HasDescription = 10
	}

	total := b.Salary + b.DirectApply + b.Freshness +
		b.DescriptionLength + b.NotAgency + b.SourceQuality + b.HasDescription

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

// hasSalary returns true when the job lists a non-zero salary range.
func hasSalary(in Input) bool {
	return in.SalaryMin > 0 || in.SalaryMax > 0
}

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

// hasSubstantiveDescription returns true when the description is non-empty and
// longer than 100 characters — a stub vs. a real JD.
func hasSubstantiveDescription(desc string) bool {
	return len(strings.TrimSpace(desc)) > 100
}

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
