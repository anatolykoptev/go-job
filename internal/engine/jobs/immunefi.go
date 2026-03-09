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
	immunefiAPIURL  = "https://immunefi.com/public-api/bounties.json"
	immunefiCacheKey = "immunefi_programs"
)

type immunefiProgram struct {
	Project   string `json:"project"`
	Slug      string `json:"slug"`
	MaxBounty int    `json:"maxBounty"`
	Assets    []struct {
		URL  string `json:"url"`
		Type string `json:"type"`
	} `json:"assets"`
}

// SearchImmunefi fetches bug bounty programs from Immunefi. Results are cached.
func SearchImmunefi(ctx context.Context, limit int) ([]engine.SecurityProgram, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}

	if cached, ok := engine.CacheLoadJSON[[]engine.SecurityProgram](ctx, immunefiCacheKey); ok {
		slog.Debug("immunefi: using cached results", slog.Int("results", len(cached)))
		if len(cached) > limit {
			cached = cached[:limit]
		}
		return cached, nil
	}

	programs, err := fetchImmunefi(ctx)
	if err != nil {
		return nil, err
	}

	engine.CacheStoreJSON(ctx, immunefiCacheKey, "", programs)
	if len(programs) > limit {
		programs = programs[:limit]
	}

	slog.Debug("immunefi: fetch complete", slog.Int("results", len(programs)))
	return programs, nil
}

func fetchImmunefi(ctx context.Context) ([]engine.SecurityProgram, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, immunefiAPIURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentBot)

	resp, err := engine.Cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("immunefi request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("immunefi returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, securityBodyLimit))
	if err != nil {
		return nil, err
	}

	return parseImmunefiResponse(body)
}

func parseImmunefiResponse(data []byte) ([]engine.SecurityProgram, error) {
	var raw []immunefiProgram
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("immunefi: parse failed: %w", err)
	}

	programs := make([]engine.SecurityProgram, 0, len(raw))
	for _, r := range raw {
		if r.Slug == "" {
			continue
		}

		targets := make([]string, 0, len(r.Assets))
		for _, a := range r.Assets {
			if a.URL != "" {
				targets = append(targets, a.URL)
			}
		}

		programs = append(programs, engine.SecurityProgram{
			Name:      r.Project,
			Platform:  "immunefi",
			URL:       "https://immunefi.com/bug-bounty/" + r.Slug + "/",
			MaxBounty: formatOptionalUSD(r.MaxBounty),
			Targets:   targets,
			Type:      "bug_bounty",
		})
	}

	return programs, nil
}
