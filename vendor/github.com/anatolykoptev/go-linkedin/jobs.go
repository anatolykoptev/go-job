package linkedin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// jobSearchDecorationID is the Rest.li decoration required by the
// voyagerJobsDashJobCards job-search endpoint. WITHOUT it → HTTP 500 (verified
// live, 2026). It rotates on LinkedIn web deploys — keep it a single const,
// easy to bump. Currently JobSearchCardsCollection-220 (proven 200 live).
const jobSearchDecorationID = "com.linkedin.voyager.dash.deco.jobs.search.JobSearchCardsCollection-220"

// geoIDWorldwide is the proven "worldwide / Remote / empty location" geoId
// (200 live). Used as the default when no mapping is known.
const geoIDWorldwide = "92000000"

// jobSearchEndpoint is the voyagerJobsDashJobCards base path.
const jobSearchEndpoint = "/voyager/api/voyagerJobsDashJobCards"

// defaultEnrichLimit is the cap on per-job detail enrichment when Enrich is
// true but EnrichLimit is zero or negative. It keeps the extra cost bounded.
const defaultEnrichLimit = 10

// resolveGeoID maps a free-text location to a LinkedIn geoId. It is
// case-insensitive and falls back to geoIDWorldwide (92000000, proven 200) for
// any unknown location — a text locationUnion (e.g. "Remote") yields HTTP 400,
// so a geoId is ALWAYS required.
//
// TODO: a LIVE typeahead geoId resolver is a follow-up. The
// /voyager/api/typeahead/hitsV2?keywords=<loc>&q=type&type=GEO form returns 404
// in 2026 (needs the correct current endpoint); for now use this map + the
// worldwide default. Only PROVEN geoIds are mapped here (worldwide/Remote/empty
// = 92000000, United States = 103644278 — both 200 live). Adding more entries
// without a live capture risks mapping a region to the wrong geoId (worse than
// the worldwide default, which at worst returns an empty result set, never a
// 400), so unmapped locations intentionally fall back to worldwide.
var geoIDMap = map[string]string{
	"":              geoIDWorldwide,
	"remote":        geoIDWorldwide,
	"worldwide":     geoIDWorldwide,
	"anywhere":      geoIDWorldwide,
	"united states": "103644278",
	"usa":           "103644278",
	"us":            "103644278",
}

func resolveGeoID(location string) string {
	if g, ok := geoIDMap[strings.ToLower(strings.TrimSpace(location))]; ok {
		return g
	}
	return geoIDWorldwide
}

// buildJobSearchEndpoint builds the 2026 Voyager Rest.li job-search query.
//
// Proven live form (all responses 200):
//
//	GET /voyager/api/voyagerJobsDashJobCards?decorationId=<JobSearchCardsCollection-220>&count=<n>&q=jobSearch&query=(keywords:<KW>,locationUnion:(geoId:<GEOID>),origin:JOB_SEARCH_PAGE_QUERY_EXPANSION)&start=<OFFSET>
//
// The `query=(...)` is a Rest.li structural literal: the `( ) : ,` MUST stay
// LITERAL (NOT percent-encoded) — only the keyword VALUE is URL-encoded
// (space → %20). A percent-encoded `(` or `:` → HTTP 400 (verified live).
// geoId is REQUIRED (a text locationUnion → 400); resolveGeoID always returns
// one. decorationId is REQUIRED (without it → HTTP 500).
func buildJobSearchEndpoint(params JobSearchParams) string {
	count := params.Limit
	if count <= 0 {
		count = 10
	}
	start := params.Start
	if start < 0 {
		start = 0
	}
	geoID := resolveGeoID(params.Location)
	// Only the keyword VALUE is encoded; structural chars stay literal.
	// url.QueryEscape encodes space as "+"; the proven live form uses "%20".
	kw := strings.ReplaceAll(url.QueryEscape(params.Query), "+", "%20")
	query := "(keywords:" + kw + ",locationUnion:(geoId:" + geoID + "),origin:JOB_SEARCH_PAGE_QUERY_EXPANSION)"
	// Param order matches the proven live form: decorationId, count, q, query, start.
	return fmt.Sprintf(
		"%s?decorationId=%s&count=%d&q=jobSearch&query=%s&start=%d",
		jobSearchEndpoint, jobSearchDecorationID, count, query, start,
	)
}

func (c *Client) SearchJobs(ctx context.Context, params JobSearchParams) ([]Job, error) {
	endpoint := buildJobSearchEndpoint(params)
	body, err := c.do(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("search jobs: %w", err)
	}
	jobs, err := parseJobSearchResults(body)
	if err != nil {
		return nil, err
	}
	if params.Enrich {
		c.enrichJobs(ctx, jobs, params.EnrichLimit)
	}
	return jobs, nil
}

// enrichJobs fills the top-K search results with fields from GetJobDetail.
// It is best-effort: a detail error for one job skips that job and continues.
// Calls are sequential so the per-session go-wowa browser lock is respected.
func (c *Client) enrichJobs(ctx context.Context, jobs []Job, limit int) {
	if limit <= 0 {
		limit = defaultEnrichLimit
	}
	if limit > len(jobs) {
		limit = len(jobs)
	}

	fetcher := c.GetJobDetail
	if c.getJobDetailFn != nil {
		fetcher = c.getJobDetailFn
	}

	for i := 0; i < limit; i++ {
		jobID := jobIDFromURN(jobs[i].URN)
		if jobID == "" {
			continue
		}
		detail, err := fetcher(ctx, jobID)
		if err != nil {
			continue
		}
		mergeJobDetail(&jobs[i], detail)
	}
}

// mergeJobDetail copies fields from a full job detail into a search Job, but
// only where the search result left the field empty. Title, URN, and ApplyURL
// from search are preserved because the search result already has them.
func mergeJobDetail(job *Job, detail *JobDetail) {
	if detail == nil {
		return
	}
	if job.Company == "" && detail.Company != "" {
		job.Company = detail.Company
	}
	if job.CompanyURN == "" && detail.CompanyURN != "" {
		job.CompanyURN = detail.CompanyURN
	}
	if job.Location == "" && detail.Location != "" {
		job.Location = detail.Location
	}
	if job.Remote == "" && detail.Remote != "" {
		job.Remote = detail.Remote
	}
	if job.PostedAt.IsZero() && !detail.PostedAt.IsZero() {
		job.PostedAt = detail.PostedAt
	}
	if job.Description == "" && detail.Description != "" {
		job.Description = detail.Description
	}
	if job.SeniorityLevel == "" && detail.SeniorityLevel != "" {
		job.SeniorityLevel = detail.SeniorityLevel
	}
	if job.EmploymentType == "" && detail.EmploymentType != "" {
		job.EmploymentType = detail.EmploymentType
	}
}

// parseJobSearchResults extracts Job entries from a normalized-JSON
// voyagerJobsDashJobCards response (Content-Type
// application/vnd.linkedin.normalized+json+2.1). The response has an
// `included[]` array mixing entity types (JobPosting, Company,
// JobPostingCard, Geo, JobSeekerJobState, JobPostingVerification, Profile).
//
// Job postings are the entities with `$type ==
// com.linkedin.voyager.dash.jobs.JobPosting`. Company names are resolved
// best-effort by matching each posting's company URN (companyDetails.company)
// to the organization.Company entities in included[]. If the link is absent or
// unresolvable, Company is left empty rather than guessed.
func parseJobSearchResults(body []byte) ([]Job, error) {
	resp, err := parseVoyagerResponse(body)
	if err != nil {
		return nil, fmt.Errorf("parse job search: %w", err)
	}

	// Index Company entities by URN for best-effort name resolution.
	const companyType = "com.linkedin.voyager.dash.organization.Company"
	companyNames := make(map[string]string)
	for _, raw := range resp.Included {
		var peek struct {
			Type      string `json:"$type"`
			EntityURN string `json:"entityUrn"`
			Name      string `json:"name"`
		}
		if json.Unmarshal(raw, &peek) != nil {
			continue
		}
		if strings.HasPrefix(peek.Type, companyType) && peek.EntityURN != "" {
			companyNames[peek.EntityURN] = peek.Name
		}
	}

	items := includedByType(resp.Included, "com.linkedin.voyager.dash.jobs.JobPosting")
	jobs := make([]Job, 0, len(items))
	for _, raw := range items {
		var j struct {
			EntityURN         string `json:"entityUrn"`
			Title             string `json:"title"`
			FormattedLoc      string `json:"formattedLocation"`
			ListedAt          int64  `json:"listedAt"`
			WorkRemoteAllowed *bool  `json:"workRemoteAllowed"`
			CompanyDetails    struct {
				Company string `json:"company"`
			} `json:"companyDetails"`
			// Some decorations put the company URN at top-level instead.
			Company string `json:"company"`
		}
		if json.Unmarshal(raw, &j) != nil || j.Title == "" {
			continue
		}
		companyURN := j.CompanyDetails.Company
		if companyURN == "" {
			companyURN = j.Company
		}
		jobID := jobIDFromURN(j.EntityURN)
		postedAt := time.Time{}
		if j.ListedAt > 0 {
			postedAt = time.UnixMilli(j.ListedAt)
		}
		jobs = append(jobs, Job{
			URN:        j.EntityURN,
			Title:      j.Title,
			Company:    companyNames[companyURN],
			CompanyURN: companyURN,
			Location:   j.FormattedLoc,
			Remote:     remoteFromFlag(j.WorkRemoteAllowed),
			PostedAt:   postedAt,
			ApplyURL:   jobViewURL(jobID),
		})
	}
	return jobs, nil
}

// jobIDFromURN extracts the numeric job ID from an entityUrn of the form
// `urn:li:fsd_jobPosting:<ID>`. Returns "" if the URN is malformed.
func jobIDFromURN(urn string) string {
	// urn:li:fsd_jobPosting:<ID>
	idx := strings.LastIndex(urn, ":")
	if idx < 0 || idx == len(urn)-1 {
		return ""
	}
	return urn[idx+1:]
}

// jobViewURL builds the human-facing job view URL from a numeric job ID.
func jobViewURL(jobID string) string {
	if jobID == "" {
		return ""
	}
	return "https://www.linkedin.com/jobs/view/" + jobID
}

// remoteFromFlag maps the workRemoteAllowed boolean to the existing Job.Remote
// vocabulary ("remote" / "onsite"). A nil pointer means the field was absent
// from the source decoration, so the result is left empty for downstream merge.
func remoteFromFlag(workRemoteAllowed *bool) string {
	if workRemoteAllowed == nil {
		return ""
	}
	if *workRemoteAllowed {
		return "remote"
	}
	return "onsite"
}

func normalizeRemote(workplaceType string) string {
	switch workplaceType {
	case "remote":
		return "remote"
	case "hybrid":
		return "hybrid"
	default:
		return "onsite"
	}
}

// GetJobDetail fetches the full detail for a single LinkedIn job posting by its
// numeric job ID (the digits in a /jobs/view/{id}/ URL or from SearchJobs URN).
//
// VALIDATE-WITH-LIVE-li_at (go-job #293): endpoint + included-entity shape
// grounded in mattmichaelree/LinkedIn-Job-Scraper (WebFullJobPosting-65
// decoration, data_variables.csv field paths) + hubertusgbecker/mcp-linkedin-
// server get_job_details return spec, NOT verified against a live Voyager
// session — re-check field paths once an account is provisioned.
func (c *Client) GetJobDetail(ctx context.Context, jobID string) (*JobDetail, error) {
	if jobID == "" {
		return nil, fmt.Errorf("get job detail: empty jobID")
	}
	endpoint := "/voyager/api/jobs/jobPostings/" + url.PathEscape(jobID) +
		"?decorationId=com.linkedin.voyager.deco.jobs.web.shared.WebFullJobPosting-65"
	body, err := c.do(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("get job detail: %w", err)
	}
	return parseJobDetail(body, jobID)
}

// parseJobDetail extracts a JobDetail from a Voyager WebFullJobPosting response.
// It is the pure-parse seam for GetJobDetail, unit-testable without a network
// connection. Missing/empty fields are omitted, not errored.
//
// VALIDATE-WITH-LIVE-li_at (go-job #293): field paths grounded in
// mattmichaelree/LinkedIn-Job-Scraper data_variables.csv (data.* paths) +
// hiring-team included-entity shape from the standard Voyager decoration
// pattern, NOT verified against a live Voyager session.
func parseJobDetail(body []byte, jobID string) (*JobDetail, error) {
	resp, err := parseVoyagerResponse(body)
	if err != nil {
		return nil, fmt.Errorf("parse job detail: %w", err)
	}

	var data struct {
		Title          string   `json:"title"`
		ApplicantCount int      `json:"applies"`
		SeniorityLevel string   `json:"formattedExperienceLevel"`
		EmploymentType string   `json:"formattedEmploymentStatus"`
		JobFunctions   []string `json:"formattedJobFunctions"`
		FormattedLoc   string   `json:"formattedLocation"`
		ListedAt       int64    `json:"listedAt"`
		WorkRemote     *bool    `json:"workRemoteAllowed"`
		WorkplaceType  string   `json:"workplaceType"`
		CompanyDetails struct {
			Company string `json:"company"`
		} `json:"companyDetails"`
		ApplyMethod struct {
			Type string `json:"$type"`
		} `json:"applyMethod"`
		Description struct {
			Text string `json:"text"`
		} `json:"description"`
	}
	// The data field is the JobPosting object itself (single-entity response).
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return nil, fmt.Errorf("parse job detail data: %w", err)
	}

	postedAt := time.Time{}
	if data.ListedAt > 0 {
		postedAt = time.UnixMilli(data.ListedAt)
	}

	detail := &JobDetail{
		JobID:          jobID,
		Title:          data.Title,
		ApplicantCount: data.ApplicantCount,
		SeniorityLevel: data.SeniorityLevel,
		EmploymentType: data.EmploymentType,
		JobFunction:    strings.Join(data.JobFunctions, ", "),
		EasyApply:      strings.Contains(data.ApplyMethod.Type, "EasyApply"),
		Location:       data.FormattedLoc,
		PostedAt:       postedAt,
		Description:    data.Description.Text,
	}

	if data.CompanyDetails.Company != "" {
		detail.CompanyURN = data.CompanyDetails.Company
		detail.Company = companyNameFromIncluded(resp.Included, data.CompanyDetails.Company)
	}

	switch {
	case data.WorkplaceType != "":
		detail.Remote = normalizeRemote(data.WorkplaceType)
	case data.WorkRemote != nil:
		detail.Remote = remoteFromFlag(data.WorkRemote)
	}

	detail.HiringTeam = hiringTeamFromIncluded(resp.Included)

	return detail, nil
}

// companyNameFromIncluded resolves a company URN to its display name by scanning
// the included array for a matching organization.Company entity.
func companyNameFromIncluded(included []json.RawMessage, companyURN string) string {
	const companyType = "com.linkedin.voyager.dash.organization.Company"
	for _, raw := range included {
		var peek struct {
			Type      string `json:"$type"`
			EntityURN string `json:"entityUrn"`
			Name      string `json:"name"`
		}
		if json.Unmarshal(raw, &peek) != nil {
			continue
		}
		if strings.HasPrefix(peek.Type, companyType) && peek.EntityURN == companyURN {
			return peek.Name
		}
	}
	return ""
}

// hiringTeamFromIncluded extracts hiring-team members from the included array.
// The Voyager WebFullJobPosting decoration may include a JobPostingHiringTeam
// entity whose elements reference JobPostingHiringMember entries, each of which
// references a Profile. This is best-effort: if the entities are absent, an
// empty slice is returned.
//
// VALIDATE-WITH-LIVE-li_at (go-job #293): hiring-team included-entity shape
// (JobPostingHiringTeam → JobPostingHiringMember → Profile) is from the
// standard Voyager decoration pattern, NOT verified against a live session.
func hiringTeamFromIncluded(included []json.RawMessage) []HiringTeamMember {
	const (
		hiringTeamType   = "com.linkedin.voyager.dash.jobs.JobPostingHiringTeam"
		hiringMemberType = "com.linkedin.voyager.dash.jobs.JobPostingHiringMember"
		profileType      = "com.linkedin.voyager.dash.identity.profile.Profile"
	)

	// Index profiles by URN for quick lookup.
	profilesByURN := make(map[string]json.RawMessage)
	for _, raw := range included {
		var peek struct {
			Type      string `json:"$type"`
			EntityURN string `json:"entityUrn"`
		}
		if json.Unmarshal(raw, &peek) != nil {
			continue
		}
		if strings.HasPrefix(peek.Type, profileType) {
			profilesByURN[peek.EntityURN] = raw
		}
	}

	// Index hiring members by URN.
	membersByURN := make(map[string]json.RawMessage)
	for _, raw := range included {
		var peek struct {
			Type      string `json:"$type"`
			EntityURN string `json:"entityUrn"`
		}
		if json.Unmarshal(raw, &peek) != nil {
			continue
		}
		if strings.HasPrefix(peek.Type, hiringMemberType) {
			membersByURN[peek.EntityURN] = raw
		}
	}

	var team []HiringTeamMember
	for _, raw := range included {
		var ht struct {
			Type     string   `json:"$type"`
			Elements []string `json:"elements"`
		}
		if json.Unmarshal(raw, &ht) != nil || !strings.HasPrefix(ht.Type, hiringTeamType) {
			continue
		}
		for _, memberURN := range ht.Elements {
			memberRaw, ok := membersByURN[memberURN]
			if !ok {
				continue
			}
			var member struct {
				ProfileURN string `json:"*profile"`
				Title      string `json:"title"`
			}
			if json.Unmarshal(memberRaw, &member) != nil {
				continue
			}
			m := HiringTeamMember{Title: member.Title}
			if profRaw, ok := profilesByURN[member.ProfileURN]; ok {
				var prof struct {
					FirstName        string `json:"firstName"`
					LastName         string `json:"lastName"`
					Headline         string `json:"headline"`
					PublicIdentifier string `json:"publicIdentifier"`
				}
				if json.Unmarshal(profRaw, &prof) == nil {
					m.Name = strings.TrimSpace(prof.FirstName + " " + prof.LastName)
					if m.Title == "" {
						m.Title = prof.Headline
					}
					if prof.PublicIdentifier != "" {
						m.ProfileURL = "https://www.linkedin.com/in/" + prof.PublicIdentifier
					}
				}
			}
			team = append(team, m)
		}
	}
	return team
}
