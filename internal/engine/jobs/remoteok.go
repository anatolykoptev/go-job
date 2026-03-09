package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/anatolykoptev/go_job/internal/engine"
)

const (
	remoteOKFreelanceCacheKey = "remoteok_jobs"
)

// SearchRemoteOKFreelance fetches remote jobs from RemoteOK as FreelanceJob.
// Cache key varies by tag: "remoteok_jobs" or "remoteok_jobs_golang".
func SearchRemoteOKFreelance(ctx context.Context, tag string, limit int) ([]engine.FreelanceJob, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	cacheKey := remoteOKFreelanceCacheKey
	if tag != "" {
		cacheKey = remoteOKFreelanceCacheKey + "_" + tag
	}

	if cached, ok := engine.CacheLoadJSON[[]engine.FreelanceJob](ctx, cacheKey); ok {
		slog.Debug("remoteok_freelance: using cached results",
			slog.Int("results", len(cached)))
		if len(cached) > limit {
			cached = cached[:limit]
		}
		return cached, nil
	}

	jobs, err := fetchRemoteOKFreelance(ctx, tag)
	if err != nil {
		return nil, err
	}

	engine.CacheStoreJSON(ctx, cacheKey, "", jobs)
	if len(jobs) > limit {
		jobs = jobs[:limit]
	}

	slog.Debug("remoteok_freelance: fetch complete",
		slog.Int("results", len(jobs)))
	return jobs, nil
}

func fetchRemoteOKFreelance(ctx context.Context, tag string) ([]engine.FreelanceJob, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	apiURL := remoteOKAPI
	if tag != "" {
		apiURL = fmt.Sprintf("%s?tag=%s", remoteOKAPI, tag)
	}

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentChrome)

	resp, err := engine.Cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("remoteok freelance request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("remoteok returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
	if err != nil {
		return nil, err
	}

	return parseRemoteOKFreelanceResponse(body)
}

func parseRemoteOKFreelanceResponse(data []byte) ([]engine.FreelanceJob, error) {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("remoteok: JSON parse failed: %w", err)
	}

	// First element is metadata — skip it.
	if len(raw) <= 1 {
		return nil, nil
	}

	jobs := make([]engine.FreelanceJob, 0, len(raw)-1)
	for _, item := range raw[1:] {
		var rj remoteOKJob
		if err := json.Unmarshal(item, &rj); err != nil {
			continue
		}
		if rj.URL == "" {
			continue
		}
		jobs = append(jobs, engine.FreelanceJob{
			Title:     rj.Position,
			Company:   rj.Company,
			URL:       rj.URL,
			Tags:      rj.Tags,
			SalaryMin: rj.SalaryMin,
			SalaryMax: rj.SalaryMax,
			Source:    "remoteok",
			Posted:    rj.Date,
			Location:  rj.Location,
		})
	}

	return jobs, nil
}
