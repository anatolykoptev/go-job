package linkedin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

func (c *Client) SearchJobs(ctx context.Context, params JobSearchParams) ([]Job, error) {
	if params.Limit <= 0 {
		params.Limit = 10
	}
	q := url.Values{}
	q.Set("keywords", params.Query)
	q.Set("count", fmt.Sprintf("%d", params.Limit))
	if params.Location != "" {
		q.Set("locationUnion", params.Location)
	}
	endpoint := "/voyager/api/voyagerJobsDashJobCards?" + q.Encode()
	body, err := c.do(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("search jobs: %w", err)
	}
	resp, err := parseVoyagerResponse(body)
	if err != nil {
		return nil, err
	}
	items := includedByType(resp.Included, "com.linkedin.voyager.dash.jobs.JobPosting")
	var jobs []Job
	for _, raw := range items {
		var j struct {
			EntityURN     string `json:"entityUrn"`
			Title         string `json:"title"`
			CompanyName   string `json:"companyName"`
			FormattedLoc  string `json:"formattedLocation"`
			ListedAt      int64  `json:"listedAt"`
			Description   string `json:"description"`
			WorkplaceType string `json:"workplaceType"`
			ApplyURL      string `json:"applyUrl"`
		}
		if json.Unmarshal(raw, &j) != nil || j.Title == "" {
			continue
		}
		jobs = append(jobs, Job{
			URN:         j.EntityURN,
			Title:       j.Title,
			Company:     j.CompanyName,
			Location:    j.FormattedLoc,
			Remote:      normalizeRemote(j.WorkplaceType),
			PostedAt:    time.UnixMilli(j.ListedAt),
			Description: j.Description,
			ApplyURL:    j.ApplyURL,
		})
	}
	return jobs, nil
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
		CompanyDetails struct {
			Company string `json:"company"`
		} `json:"companyDetails"`
		ApplyMethod struct {
			Type string `json:"$type"`
		} `json:"applyMethod"`
	}
	// The data field is the JobPosting object itself (single-entity response).
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return nil, fmt.Errorf("parse job detail data: %w", err)
	}

	detail := &JobDetail{
		JobID:          jobID,
		Title:          data.Title,
		ApplicantCount: data.ApplicantCount,
		SeniorityLevel: data.SeniorityLevel,
		EmploymentType: data.EmploymentType,
		JobFunction:    strings.Join(data.JobFunctions, ", "),
		EasyApply:      strings.Contains(data.ApplyMethod.Type, "EasyApply"),
	}

	if data.CompanyDetails.Company != "" {
		detail.Company = companyNameFromIncluded(resp.Included, data.CompanyDetails.Company)
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
