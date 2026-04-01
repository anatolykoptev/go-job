package linkedin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
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
