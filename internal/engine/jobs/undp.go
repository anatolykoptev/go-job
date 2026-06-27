package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// UNDP runs on Oracle Fusion HCM (Candidate Experience site CX_1) hosted at
// estm.fa.em2.oraclecloud.com. The same SaaS instance also serves OHCHR's
// internal recruiting, but the CX_1 site URL filter restricts the response
// to UNDP postings.

const undpAPI = "https://estm.fa.em2.oraclecloud.com/hcmRestApi/resources/latest/recruitingCEJobRequisitions"
const undpJobURLFormat = "https://estm.fa.em2.oraclecloud.com/hcmUI/CandidateExperience/en/sites/CX_1/job/%s"

type undpRequisition struct {
	ID                     string `json:"Id"`
	Title                  string `json:"Title"`
	PostedDate             string `json:"PostedDate"`
	PostingEndDate         string `json:"PostingEndDate"`
	Language               string `json:"Language"`
	PrimaryLocation        string `json:"PrimaryLocation"`
	PrimaryLocationCountry string `json:"PrimaryLocationCountry"`
	WorkplaceTypeCode      string `json:"WorkplaceTypeCode"`
	JobFamily              string `json:"JobFamily"`
	JobFunction            string `json:"JobFunction"`
	WorkerType             string `json:"WorkerType"`
	ContractType           string `json:"ContractType"`
	ManagerLevel           string `json:"ManagerLevel"`
	JobSchedule            string `json:"JobSchedule"`
	JobShift               string `json:"JobShift"`
	JobType                string `json:"JobType"`
	StudyLevel             string `json:"StudyLevel"`
	Organization           string `json:"Organization"`
	Department             string `json:"Department"`
	BusinessUnit           string `json:"BusinessUnit"`
	ShortDescriptionStr    string `json:"ShortDescriptionStr"`
	ExternalDescriptionStr string `json:"ExternalDescriptionStr"`
}

type undpRequisitionList struct {
	Items []undpRequisition `json:"items"`
	Count int               `json:"count"`
}

type undpSearchItem struct {
	RequisitionList undpRequisitionList `json:"requisitionList"`
	TotalJobsCount  int                 `json:"TotalJobsCount"`
}

type undpResponse struct {
	Items []undpSearchItem `json:"items"`
}

// SearchUNDPJobs queries the UNDP Oracle HCM API and returns up to limit matching
// requisitions as engine.SearxngResult. The query is passed as a free-text keyword
// to the Oracle recruiting service.
func SearchUNDPJobs(ctx context.Context, query, _location string, limit int) ([]engine.SearxngResult, error) {
	if limit <= 0 {
		limit = 15
	}
	if limit > 50 {
		limit = 50
	}

	// Oracle CX finder= expression has a very specific shape: comma-separated
	// at the top level, but the first separator after `findReqs` MUST be a
	// literal `;` (not `%3B`), and the facets-list values inside also use `;`.
	// url.Values.Encode() over-escapes both, so we assemble the query string
	// manually and only percent-encode the keyword value itself.
	facetsList := "LOCATIONS;WORK_LOCATIONS;WORKPLACE_TYPES;TITLES;CATEGORIES;ORGANIZATIONS;POSTING_DATES;FLEX_FIELDS"
	finder := fmt.Sprintf(
		"findReqs;siteNumber=CX_1,facetsList=%s,limit=%d,sortBy=POSTING_DATES_DESC",
		facetsList, limit,
	)
	if q := strings.TrimSpace(query); q != "" {
		// SECURITY INVARIANT: url.QueryEscape must percent-encode `,` `;` `&` `=`
		// so the user-supplied keyword cannot break out of the keyword= value
		// and inject extra finder params (e.g. limit=999, sortBy=anything, or
		// a different facets list). url.PathEscape would NOT be safe here
		// because it leaves `,` raw — do not refactor to it.
		finder += ",keyword=" + url.QueryEscape(q)
	}

	reqURL := undpAPI + "?onlyData=true" +
		"&expand=requisitionList.secondaryLocations,flexFieldsFacet.values" +
		"&finder=" + finder

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("REST-Framework-Version", "7")
	req.Header.Set("User-Agent", engine.UserAgentBot)

	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		return engine.Cfg.HTTPClient.Do(req) //nolint:gosec // public Oracle HCM API
	})
	if err != nil {
		return nil, fmt.Errorf("undp request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("undp API status %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, err
	}

	var ur undpResponse
	if err := json.Unmarshal(raw, &ur); err != nil {
		return nil, fmt.Errorf("undp parse: %w", err)
	}
	if len(ur.Items) == 0 {
		return nil, nil
	}

	requisitions := ur.Items[0].RequisitionList.Items
	out := make([]engine.SearxngResult, 0, len(requisitions))
	for _, j := range requisitions {
		out = append(out, undpRequisitionToResult(j))
	}
	slog.Debug("undp: search complete", slog.Int("results", len(out)))
	return out, nil
}

func undpRequisitionToResult(j undpRequisition) engine.SearxngResult {
	var contentB strings.Builder
	switch {
	case j.Organization != "":
		fmt.Fprintf(&contentB, "**Org:** %s", j.Organization)
	case j.BusinessUnit != "":
		fmt.Fprintf(&contentB, "**Org:** %s", j.BusinessUnit)
	default:
		contentB.WriteString("**Org:** UNDP")
	}
	loc := j.PrimaryLocation
	if loc == "" && j.PrimaryLocationCountry != "" {
		loc = j.PrimaryLocationCountry
	}
	if loc != "" {
		fmt.Fprintf(&contentB, " | **Location:** %s", loc)
	}
	if j.WorkplaceTypeCode != "" {
		fmt.Fprintf(&contentB, " | **Workplace:** %s", j.WorkplaceTypeCode)
	}
	if j.JobFunction != "" {
		fmt.Fprintf(&contentB, " | **Function:** %s", j.JobFunction)
	}
	if j.ContractType != "" {
		fmt.Fprintf(&contentB, " | **Contract:** %s", j.ContractType)
	}
	if j.PostedDate != "" {
		fmt.Fprintf(&contentB, " | **Posted:** %s", j.PostedDate)
	}
	if j.PostingEndDate != "" {
		fmt.Fprintf(&contentB, " | **Closes:** %s", j.PostingEndDate)
	}
	if desc := j.ShortDescriptionStr; desc != "" {
		contentB.WriteString("\n\n")
		contentB.WriteString(engine.TruncateRunes(engine.CleanHTML(desc), 600, "..."))
	} else if desc := j.ExternalDescriptionStr; desc != "" {
		contentB.WriteString("\n\n")
		contentB.WriteString(engine.TruncateRunes(engine.CleanHTML(desc), 600, "..."))
	}

	return engine.SearxngResult{
		Title:   j.Title,
		Content: contentB.String(),
		URL:     fmt.Sprintf(undpJobURLFormat, j.ID),
		Score:   0.9,
		Metadata: map[string]string{
			memdbKeySource:       sourceUNDP,
			"org":          firstNonEmpty(j.Organization, j.BusinessUnit, "UNDP"),
			"location":     loc, //nolint:goconst
			"country":      j.PrimaryLocationCountry,
			"workplace":    j.WorkplaceTypeCode,
			"contract":     j.ContractType,
			"posted":       j.PostedDate,
			"closes":       j.PostingEndDate,
			"job_family":   j.JobFamily,
			"job_function": j.JobFunction,
		},
	}
}

func firstNonEmpty(parts ...string) string {
	for _, p := range parts {
		if p != "" {
			return p
		}
	}
	return ""
}
