package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/anatolykoptev/go_job/internal/engine"
)

const (
	lightningAPIURL        = "https://app.lightningbounties.com/api/issues/?distinct_issues=true&skip=0&limit=100"
	lightningScrapeCacheKey = "lightning_scrape"
	btcUSDRate             = 80000
	satsPerBTC             = 100_000_000
)

type lightningIssue struct {
	ID             string  `json:"id"`
	Title          string  `json:"title"`
	HTMLURL        string  `json:"html_url"`
	IsClosed       bool    `json:"is_closed"`
	WinnerID       *string `json:"winner_id"`
	TotalRewardSats int    `json:"total_reward_sats"`
	RepositoryData struct {
		FullName string `json:"full_name"`
	} `json:"repository_data"`
	CreatedAt string `json:"created_at"`
}

// SearchLightning fetches open bounties from Lightning Bounties. Results are cached.
func SearchLightning(ctx context.Context, limit int) ([]engine.BountyListing, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	if cached, ok := engine.CacheLoadJSON[[]engine.BountyListing](ctx, lightningScrapeCacheKey); ok {
		slog.Debug("lightning: using cached results", slog.Int("results", len(cached)))
		if len(cached) > limit {
			cached = cached[:limit]
		}
		return cached, nil
	}

	bounties, err := fetchLightning(ctx)
	if err != nil {
		return nil, err
	}

	engine.CacheStoreJSON(ctx, lightningScrapeCacheKey, "", bounties)
	if len(bounties) > limit {
		bounties = bounties[:limit]
	}

	slog.Debug("lightning: fetch complete", slog.Int("results", len(bounties)))
	return bounties, nil
}

func fetchLightning(ctx context.Context) ([]engine.BountyListing, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, lightningAPIURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentChrome)

	resp, err := engine.Cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("lightning request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lightning returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, err
	}

	return parseLightningResponse(body)
}

func parseLightningResponse(data []byte) ([]engine.BountyListing, error) {
	var issues []lightningIssue
	if err := json.Unmarshal(data, &issues); err != nil {
		return nil, fmt.Errorf("lightning: JSON parse failed: %w", err)
	}

	bounties := make([]engine.BountyListing, 0, len(issues))
	for _, issue := range issues {
		if issue.IsClosed || issue.WinnerID != nil || issue.HTMLURL == "" {
			continue
		}

		cents := issue.TotalRewardSats * btcUSDRate / satsPerBTC * 100
		amount := formatCentsUSD(cents)

		issueNum := ""
		if _, _, num, ok := ParseGitHubIssueURL(issue.HTMLURL); ok {
			issueNum = "#" + strconv.Itoa(num)
		}

		bounties = append(bounties, engine.BountyListing{
			Title:    issue.Title,
			Org:      issue.RepositoryData.FullName,
			URL:      issue.HTMLURL,
			Amount:   amount,
			Currency: "USD",
			Source:   "lightning",
			IssueNum: issueNum,
		})
	}

	return bounties, nil
}
