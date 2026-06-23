package jobs

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/hunt"
)

// mapAlgoraStatus converts Algora-specific status strings to hunt lifecycle constants.
// "open" / "" → StatusOpen; "claimed" / statusCompleted → StatusMerged;
// statusClosed / "cancelled" → StatusClosed.
func mapAlgoraStatus(s string) string {
	switch strings.ToLower(s) {
	case "claimed", statusCompleted:
		return hunt.StatusMerged
	case statusClosed, "cancelled":
		return hunt.StatusClosed
	default:
		return hunt.StatusOpen
	}
}

// BountyListingToHunt converts a BountyListing (widest bounty type) to a hunt.Bounty.
// AmountCents is parsed from the human-readable Amount string (e.g. "$50k" → 5000000).
// IssueNum "#42" prefix is stripped before Atoi — all 7 sources emit the hash prefix.
// Status is mapped from Algora-style strings to hunt status constants.
// Raw is populated with the serialized source struct for audit trail.
func BountyListingToHunt(b engine.BountyListing) hunt.Bounty {
	amountCents := parseDollarCents(b.Amount)
	// Sources emit IssueNum as "#42" — strip the prefix before parsing.
	issueNum, _ := strconv.Atoi(strings.TrimPrefix(b.IssueNum, "#"))
	rawJSON, _ := json.Marshal(b) // ignore err — Raw is optional audit trail
	return hunt.Bounty{
		DedupHash:   hunt.DedupHashForSource(b.URL, b.Source),
		Title:       b.Title,
		URL:         b.URL,
		Org:         b.Org,
		Source:      b.Source,
		AmountCents: amountCents,
		Currency:    b.Currency,
		IssueNumber: issueNum,
		Skills:      b.Skills,
		Relevance:   b.Relevance,
		Status:      mapAlgoraStatus(b.Status),
		PostedAt:    parseTimestamp(b.Posted),
		Raw:         rawJSON,
	}
}

// SourceFromURL classifies a job URL to its origin scraper. Used as a
// post-LLM safety net so hunt_jobs.source stays correct even when the
// extractor enumerates "other" for sources outside its training set.
// Returns empty string when the URL does not match any known portal.
func SourceFromURL(jobURL string) string {
	u := strings.ToLower(jobURL)
	switch {
	case strings.Contains(u, "careers.un.org"):
		return sourceInspira
	case strings.Contains(u, "estm.fa.em2.oraclecloud.com/hcmui/candidateexperience") ||
		strings.Contains(u, "jobs.undp.org"):
		return sourceUNDP
	case strings.Contains(u, "linkedin.com/jobs"):
		return sourceLinkedIn
	case strings.Contains(u, "boards.greenhouse.io") || strings.Contains(u, "boards-api.greenhouse.io"):
		return "greenhouse"
	case strings.Contains(u, "jobs.lever.co"):
		return "lever"
	case strings.Contains(u, "jobs.ashbyhq.com"):
		return "ashby"
	case strings.Contains(u, "workatastartup.com"):
		return "yc"
	case strings.Contains(u, "news.ycombinator.com"):
		return "hn"
	case strings.Contains(u, "indeed.com"):
		return "indeed"
	case strings.Contains(u, "career.habr.com") || strings.Contains(u, "habr.com/ru/jobs"):
		return sourceHabr
	case strings.Contains(u, "remoteok.com"):
		return sourceRemoteOK
	case strings.Contains(u, "weworkremotely.com"):
		return sourceWeWorkRemotely
	case strings.Contains(u, "remotive.com"):
		return "remotive"
	case strings.Contains(u, "algora.io") && strings.Contains(u, "/job/"):
		return "algora-jobs"
	}
	return ""
}

// JobListingToHunt converts a JobListing to a hunt.Job.
// Raw is populated with the serialized source struct for audit trail.
// Source is URL-derived when the LLM emitted an empty or generic "other"
// classification so hunt_jobs queries can filter by origin reliably.
func JobListingToHunt(j engine.JobListing) hunt.Job {
	salMin := 0
	if j.SalaryMin != nil {
		salMin = *j.SalaryMin
	}
	salMax := 0
	if j.SalaryMax != nil {
		salMax = *j.SalaryMax
	}
	source := j.Source
	if source == "" || source == "other" {
		if derived := SourceFromURL(j.URL); derived != "" {
			source = derived
		}
	}
	rawJSON, _ := json.Marshal(j)
	return hunt.Job{
		DedupHash:      hunt.DedupHash(j.URL),
		Title:          j.Title,
		Company:        j.Company,
		URL:            j.URL,
		Source:         source,
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
		PostedAt:       parseTimestamp(j.Posted),
		Raw:            rawJSON,
	}
}

// RemoteJobListingToHunt converts a RemoteJobListing (from RemoteOK/Remotive/WeWorkRemotely)
// to a hunt.Job. Source identifies the origin scraper (sourceRemoteOK, "remotive", "weworkremotely").
// Platform is set to Source in Phase 1; TODO(phase2): split platform vs source convention.
// Raw is populated with the serialized source struct for audit trail.
func RemoteJobListingToHunt(r engine.RemoteJobListing) hunt.Job {
	rawJSON, _ := json.Marshal(r)
	return hunt.Job{
		DedupHash: hunt.DedupHash(r.URL),
		Title:     r.Title,
		Company:   r.Company,
		URL:       r.URL,
		Source:    r.Source, // origin scraper: sourceRemoteOK, "remotive", "weworkremotely"
		Location:  r.Location,
		JobType:   r.JobType,
		Tags:      r.Tags,
		PostedAt:  parseTimestamp(r.Posted),
		Raw:       rawJSON,
	}
}

// FreelanceProjectToHunt converts a FreelanceProject to a hunt.Freelance.
// Source = Platform in Phase 1 (same field from scraper). TODO(phase2): split platform vs source.
// Raw is populated with the serialized source struct for audit trail.
func FreelanceProjectToHunt(f engine.FreelanceProject) hunt.Freelance {
	rawJSON, _ := json.Marshal(f)
	return hunt.Freelance{
		DedupHash:   hunt.DedupHash(f.URL),
		Title:       f.Title,
		URL:         f.URL,
		Platform:    f.Platform, // origin platform: "upwork", "freelancer"
		Source:      f.Platform, // TODO(phase2): split Source (scraper ID) from Platform (UI name)
		BudgetRaw:   f.Budget,
		Skills:      f.Skills,
		Description: f.Description,
		ClientInfo:  f.ClientInfo,
		PostedAt:    parseTimestamp(f.Posted),
		Raw:         rawJSON,
	}
}

// FreelanceJobToHunt converts a FreelanceJob (remoteok/himalayas type) to a hunt.Freelance.
// Source = origin scraper (sourceRemoteOK, "himalayas"). Platform = same in Phase 1.
// TODO(phase2): split platform vs source convention once dedicated scrapers have stable IDs.
// Raw is populated with the serialized source struct for audit trail.
func FreelanceJobToHunt(f engine.FreelanceJob) hunt.Freelance {
	rawJSON, _ := json.Marshal(f)
	return hunt.Freelance{
		DedupHash: hunt.DedupHash(f.URL),
		Title:     f.Title,
		URL:       f.URL,
		Platform:  f.Source, // TODO(phase2): platform = brand name, source = scraper ID
		Source:    f.Source,
		BudgetMin: f.SalaryMin,
		BudgetMax: f.SalaryMax,
		Location:  f.Location,
		Tags:      f.Tags,
		PostedAt:  parseTimestamp(f.Posted),
		Raw:       rawJSON,
	}
}

// SecurityProgramToHunt converts a SecurityProgram to a hunt.Security.
// Min/MaxBounty are parsed from human-readable strings (e.g. "$50,000").
// Archived=true (Sherlock) maps to StatusArchived.
// Raw is populated with the serialized source struct for audit trail.
func SecurityProgramToHunt(s engine.SecurityProgram) hunt.Security {
	minB := parseDollarAmount(s.MinBounty)
	maxB := parseDollarAmount(s.MaxBounty)
	status := hunt.StatusOpen
	if s.Archived {
		status = hunt.StatusArchived
	}
	rawJSON, _ := json.Marshal(s)
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
		Status:      status,
		Raw:         rawJSON,
	}
}

// parseDollarAmount converts strings like "$50k", "$5,000", "$1.5M" into integer dollars.
// Returns 0 if unparseable or negative.
func parseDollarAmount(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	s = strings.TrimPrefix(s, "$")
	s = strings.ReplaceAll(s, ",", "")
	s = strings.TrimSpace(s)

	// Handle ranges like "100-500": take the first number.
	if idx := strings.IndexAny(s, "-–"); idx > 0 {
		s = s[:idx]
	}

	multiplier := 1
	if last := len(s) - 1; last >= 0 {
		switch s[last] {
		case 'k', 'K':
			multiplier = 1_000
			s = s[:last]
		case 'm', 'M':
			multiplier = 1_000_000
			s = s[:last]
		}
	}

	// Now s is "50" or "1.5" — handle decimals via ParseFloat.
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f < 0 {
		return 0
	}
	return int(f * float64(multiplier))
}

// parseDollarCents converts a dollar string to cents (e.g. "$50k" → 5_000_000).
func parseDollarCents(s string) int64 {
	dollars := parseDollarAmount(s)
	return int64(dollars) * 100
}

// parseTimestamp attempts RFC3339, ISO8601 short, and common date formats.
// Returns nil on failure so the posted_at column stays NULL in Postgres.
func parseTimestamp(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	layouts := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
		"2006-01-02",
		time.RFC1123,
		time.RFC1123Z,
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}

// SearxngResultToHuntJob converts a raw SearxngResult (from the ATS discovery
// + fetch pipeline) to a hunt.Job for the scheduled ingest worker.
// platform should be one of "greenhouse", "lever", "ashby".
// Title / company are best-effort from the snippet; the URL is the authoritative
// dedup key.
func SearxngResultToHuntJob(r engine.SearxngResult, platform string) hunt.Job {
	rawJSON, _ := json.Marshal(r)
	return hunt.Job{
		DedupHash:   hunt.DedupHash(r.URL),
		Title:       r.Title,
		URL:         r.URL,
		Source:      platform,
		Description: r.Content,
		Raw:         rawJSON,
	}
}
