package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// Habr Career (career.habr.com) — Russian-language job board for IT professionals.
// Uses the public JSON API (no auth required).

const habrCareerAPIBase = "https://career.habr.com/api/frontend/vacancies"

// habrVacanciesResponse is the top-level API response.
type habrVacanciesResponse struct {
	List []habrVacancy `json:"list"`
	Meta struct {
		TotalCount int `json:"totalCount"`
	} `json:"meta"`
}

// habrVacancy is a single vacancy from the Habr Career API.
type habrVacancy struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	Href  string `json:"href"`
	Company struct {
		Title string `json:"title"`
		Href  string `json:"href"`
	} `json:"company"`
	Salary struct {
		From     *int   `json:"from"`
		To       *int   `json:"to"`
		Currency string `json:"currency"`
	} `json:"salary"`
	Skills []struct {
		Title string `json:"title"`
	} `json:"skills"`
	Locations []struct {
		Title string `json:"title"`
	} `json:"locations"`
	RemoteWork bool   `json:"remoteWork"`
	PublishedAt string `json:"publishedAt"`
	Employment struct {
		Title string `json:"title"`
	} `json:"employment"`
}

// SearchHabrJobs searches Habr Career for IT job listings.
func SearchHabrJobs(ctx context.Context, query, location string, limit int) ([]engine.SearxngResult, error) {
	engine.IncrHabrRequests()

	if limit <= 0 || limit > 30 {
		limit = 15
	}

	u, err := url.Parse(habrCareerAPIBase)
	if err != nil {
		return nil, err
	}

	q := u.Query()
	q.Set("q", query)
	q.Set("per_page", strconv.Itoa(limit))
	q.Set("page", "1")
	if location != "" {
		q.Set("locations[]", location)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentBot)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		return engine.Cfg.HTTPClient.Do(req)
	})
	if err != nil {
		return nil, fmt.Errorf("habr career API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("habr career API status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, err
	}

	var apiResp habrVacanciesResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("habr career parse: %w", err)
	}

	results := make([]engine.SearxngResult, 0, len(apiResp.List))
	for _, v := range apiResp.List {
		if v.Title == "" {
			continue
		}

		jobURL := v.Href
		if !strings.HasPrefix(jobURL, "http") {
			jobURL = "https://career.habr.com" + jobURL
		}

		var contentParts []string
		contentParts = append(contentParts, "**Source:** Хабр Карьера")

		if v.Company.Title != "" {
			contentParts = append(contentParts, "**Company:** "+v.Company.Title)
		}

		// Locations
		var locs []string
		for _, l := range v.Locations {
			if l.Title != "" {
				locs = append(locs, l.Title)
			}
		}
		if v.RemoteWork {
			locs = append(locs, "Remote")
		}
		if len(locs) > 0 {
			contentParts = append(contentParts, "**Location:** "+strings.Join(locs, ", "))
		}

		// Salary
		if v.Salary.From != nil || v.Salary.To != nil {
			salary := formatHabrSalary(v.Salary.From, v.Salary.To, v.Salary.Currency)
			contentParts = append(contentParts, "**Salary:** "+salary)
		}

		// Skills
		var skills []string
		for _, s := range v.Skills {
			if s.Title != "" {
				skills = append(skills, s.Title)
			}
		}
		if len(skills) > 0 {
			contentParts = append(contentParts, "**Skills:** "+strings.Join(skills, ", "))
		}

		if v.Employment.Title != "" {
			contentParts = append(contentParts, "**Type:** "+v.Employment.Title)
		}

		if v.PublishedAt != "" && len(v.PublishedAt) >= 10 {
			contentParts = append(contentParts, "**Posted:** "+v.PublishedAt[:10])
		}

		title := v.Title
		if v.Company.Title != "" {
			title = v.Title + " at " + v.Company.Title
		}

		results = append(results, engine.SearxngResult{
			Title:   title,
			Content: strings.Join(contentParts, " | "),
			URL:     jobURL,
			Score:   0.9,
		})
	}

	slog.Debug("habr: search complete", slog.Int("results", len(results)))
	return results, nil
}

// formatHabrSalary formats salary range from Habr Career API.
func formatHabrSalary(from, to *int, currency string) string {
	cur := currency
	if cur == "" {
		cur = "RUB"
	}
	switch {
	case from != nil && to != nil:
		return fmt.Sprintf("%d – %d %s", *from, *to, cur)
	case from != nil:
		return fmt.Sprintf("от %d %s", *from, cur)
	case to != nil:
		return fmt.Sprintf("до %d %s", *to, cur)
	default:
		return ""
	}
}
