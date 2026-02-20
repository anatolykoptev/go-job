package sources

import (
	"github.com/anatolykoptev/go_job/internal/engine"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const freelancerAPIBase = "https://www.freelancer.com/api/projects/0.1/projects/active"

// freelancerAPIResponse is the top-level response from the Freelancer API.
type freelancerAPIResponse struct {
	Status string `json:"status"`
	Result struct {
		Projects []freelancerAPIProject `json:"projects"`
	} `json:"result"`
}

// freelancerAPIProject is a single project from the Freelancer API.
type freelancerAPIProject struct {
	ID           int64                `json:"id"`
	Title        string               `json:"title"`
	SEOUrl       string               `json:"seo_url"`
	Description  string               `json:"preview_description"`
	Budget       freelancerBudget     `json:"budget"`
	Currency     freelancerCurrency   `json:"currency"`
	BidStats     freelancerBidStats   `json:"bid_stats"`
	Jobs         []freelancerJob      `json:"jobs"`
	Type         string               `json:"type"`
	TimeSubmitted float64             `json:"time_submitted"`
}

type freelancerBudget struct {
	Minimum float64 `json:"minimum"`
	Maximum float64 `json:"maximum"`
}

type freelancerCurrency struct {
	Code string `json:"code"`
	Sign string `json:"sign"`
}

type freelancerBidStats struct {
	BidCount int `json:"bid_count"`
	BidAvg   float64 `json:"bid_avg"`
}

type freelancerJob struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// SearchFreelancerAPI queries the Freelancer.com public API for active projects.
func SearchFreelancerAPI(ctx context.Context, query string, limit int) ([]engine.FreelanceProject, error) {
	engine.IncrFreelancerAPIRequests()

	if limit <= 0 || limit > 20 {
		limit = 10
	}

	u, err := url.Parse(freelancerAPIBase)
	if err != nil {
		return nil, err
	}

	q := u.Query()
	q.Set("query", query)
	q.Set("limit", fmt.Sprintf("%d", limit))
	q.Set("compact", "true")
	q.Set("job_details", "true")
	q.Set("full_description", "true")
	u.RawQuery = q.Encode()

	ctx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", engine.RandomUserAgent())
	req.Header.Set("Accept", "application/json")

	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		return engine.Cfg.HTTPClient.Do(req)
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("freelancer API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return nil, err
	}

	return parseFreelancerResponse(body)
}

// parseFreelancerResponse parses the Freelancer API JSON into engine.FreelanceProject slice.
func parseFreelancerResponse(body []byte) ([]engine.FreelanceProject, error) {
	var apiResp freelancerAPIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("freelancer API parse error: %w", err)
	}

	if apiResp.Status != "success" {
		return nil, fmt.Errorf("freelancer API status: %s", apiResp.Status)
	}

	projects := make([]engine.FreelanceProject, 0, len(apiResp.Result.Projects))
	for _, p := range apiResp.Result.Projects {
		projectURL := fmt.Sprintf("https://www.freelancer.com/projects/%s", p.SEOUrl)

		skills := make([]string, 0, len(p.Jobs))
		for _, j := range p.Jobs {
			skills = append(skills, j.Name)
		}

		posted := ""
		if p.TimeSubmitted > 0 {
			t := time.Unix(int64(p.TimeSubmitted), 0)
			posted = t.UTC().Format("2006-01-02")
		}

		projects = append(projects, engine.FreelanceProject{
			Title:       p.Title,
			URL:         projectURL,
			Platform:    "freelancer",
			Budget:      formatBudget(p.Budget, p.Currency, p.Type),
			Skills:      skills,
			Description: engine.TruncateAtWord(p.Description, 300),
			Posted:      posted,
		})
	}

	slog.Debug("freelancer API results", slog.Int("count", len(projects)))
	return projects, nil
}

// formatBudget formats the budget range with currency and type.
func formatBudget(b freelancerBudget, c freelancerCurrency, projectType string) string {
	if b.Minimum == 0 && b.Maximum == 0 {
		return "not specified"
	}

	currency := c.Code
	if currency == "" {
		currency = "USD"
	}

	suffix := ""
	if projectType == "hourly" {
		suffix = "/hr"
	}

	if b.Minimum == b.Maximum {
		return fmt.Sprintf("%s%.0f %s%s", c.Sign, b.Maximum, currency, suffix)
	}
	return fmt.Sprintf("%s%.0f-%s%.0f %s%s", c.Sign, b.Minimum, c.Sign, b.Maximum, currency, suffix)
}

// FreelancerProjectsToSearxngResults converts API projects to pipeline-compatible format.
// Pre-formatted content includes budget, skills, bids, and description.
func FreelancerProjectsToSearxngResults(projects []engine.FreelanceProject) []engine.SearxngResult {
	results := make([]engine.SearxngResult, 0, len(projects))
	for _, p := range projects {
		var content strings.Builder
		content.WriteString("**Budget:** " + p.Budget)
		if len(p.Skills) > 0 {
			content.WriteString(" | **Skills:** " + strings.Join(p.Skills, ", "))
		}
		if p.Posted != "" {
			content.WriteString(" | **Posted:** " + p.Posted)
		}
		if p.Description != "" {
			content.WriteString("\n" + p.Description)
		}

		results = append(results, engine.SearxngResult{
			Title:   fmt.Sprintf("%q on Freelancer", p.Title),
			Content: content.String(),
			URL:     p.URL,
			Score:   1.0,
		})
	}
	return results
}
