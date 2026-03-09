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
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
)

const (
	himalayasAPIURL   = "https://himalayas.app/jobs/api"
	himalayasCacheKey = "himalayas_jobs"
)

type himalayasResponse struct {
	Jobs  []himalayasJob `json:"jobs"`
	Total int            `json:"total"`
}

type himalayasJob struct {
	Title          string          `json:"title"`
	CompanyName    string          `json:"companyName"`
	ApplicationURL string          `json:"applicationUrl"`
	Categories     []string        `json:"categories"`
	Seniority      []string        `json:"seniority"`
	MinSalary      int             `json:"minSalary"`
	MaxSalary      int             `json:"maxSalary"`
	PubDate        json.RawMessage `json:"pubDate"`
	Excerpt        string          `json:"excerpt"`
}

// SearchHimalayas fetches jobs from Himalayas. Results are cached.
func SearchHimalayas(ctx context.Context, query string, limit int) ([]engine.FreelanceJob, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	cacheKey := himalayasCacheKey
	if query != "" {
		cacheKey = himalayasCacheKey + "_" + query
	}

	if cached, ok := engine.CacheLoadJSON[[]engine.FreelanceJob](ctx, cacheKey); ok {
		slog.Debug("himalayas: using cached results", slog.Int("results", len(cached)))
		if len(cached) > limit {
			cached = cached[:limit]
		}
		return cached, nil
	}

	jobs, err := fetchHimalayas(ctx, query, limit)
	if err != nil {
		return nil, err
	}

	engine.CacheStoreJSON(ctx, cacheKey, "", jobs)
	if len(jobs) > limit {
		jobs = jobs[:limit]
	}

	slog.Debug("himalayas: fetch complete", slog.Int("results", len(jobs)))
	return jobs, nil
}

func fetchHimalayas(ctx context.Context, query string, limit int) ([]engine.FreelanceJob, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	params := url.Values{}
	if query != "" {
		params.Set("q", query)
	}
	params.Set("limit", fmt.Sprintf("%d", limit))

	apiURL := himalayasAPIURL + "?" + params.Encode()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentChrome)

	resp, err := engine.Cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("himalayas request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("himalayas returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
	if err != nil {
		return nil, err
	}

	return parseHimalayasResponse(body)
}

func parseHimalayasResponse(data []byte) ([]engine.FreelanceJob, error) {
	var resp himalayasResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("himalayas: JSON parse failed: %w", err)
	}

	if len(resp.Jobs) == 0 {
		return nil, nil
	}

	jobs := make([]engine.FreelanceJob, 0, len(resp.Jobs))
	for _, hj := range resp.Jobs {
		if hj.ApplicationURL == "" {
			continue
		}
		tags := hj.Categories
		if len(hj.Seniority) > 0 {
			tags = append(tags, hj.Seniority...)
		}
		jobs = append(jobs, engine.FreelanceJob{
			Title:     hj.Title,
			Company:   hj.CompanyName,
			URL:       hj.ApplicationURL,
			Tags:      tags,
			SalaryMin: hj.MinSalary,
			SalaryMax: hj.MaxSalary,
			Source:    "himalayas",
			Posted:    parsePubDate(hj.PubDate),
		})
	}

	return jobs, nil
}

// parsePubDate handles pubDate as either a JSON string or a Unix timestamp number.
func parsePubDate(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 1 && s[0] == '"' {
		var str string
		if json.Unmarshal(raw, &str) == nil {
			return str
		}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n > 1e12 {
			n /= 1000
		}
		return time.Unix(n, 0).UTC().Format(time.RFC3339)
	}
	return s
}
