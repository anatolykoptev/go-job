package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
)

const (
	bossAPIURL         = "https://api.boss.dev/rpc/issues/gh/unsolved"
	bossScrapeCacheKey = "boss_scrape"
)

type bossIssue struct {
	GID    string         `json:"gid"`
	HID    string         `json:"hId"`
	SByC   map[string]int `json:"sByC"`
	Status string         `json:"status"`
	Title  string         `json:"title"`
	URL    string         `json:"url"`
	USD    int            `json:"usd"`
}

// SearchBoss fetches open bounties from Boss.dev. Results are cached.
func SearchBoss(ctx context.Context, limit int) ([]engine.BountyListing, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	if cached, ok := engine.CacheLoadJSON[[]engine.BountyListing](ctx, bossScrapeCacheKey); ok {
		slog.Debug("boss: using cached results", slog.Int("results", len(cached)))
		if len(cached) > limit {
			cached = cached[:limit]
		}
		return cached, nil
	}

	bounties, err := fetchBoss(ctx)
	if err != nil {
		return nil, err
	}

	engine.CacheStoreJSON(ctx, bossScrapeCacheKey, "", bounties)
	if len(bounties) > limit {
		bounties = bounties[:limit]
	}

	slog.Debug("boss: fetch complete", slog.Int("results", len(bounties)))
	return bounties, nil
}

func fetchBoss(ctx context.Context) ([]engine.BountyListing, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, bossAPIURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentChrome)

	resp, err := engine.Cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("boss request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("boss returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, err
	}

	return parseBossResponse(body)
}

func parseBossResponse(data []byte) ([]engine.BountyListing, error) {
	var issues []bossIssue
	if err := json.Unmarshal(data, &issues); err != nil {
		return nil, fmt.Errorf("boss: JSON parse failed: %w", err)
	}

	bounties := make([]engine.BountyListing, 0, len(issues))
	for _, issue := range issues {
		if issue.URL == "" || issue.Status != "open" {
			continue
		}

		amount := formatCentsUSD(issue.USD * 100)
		org, issueNum := parseBossHID(issue.HID)

		bounties = append(bounties, engine.BountyListing{
			Title:    issue.Title,
			Org:      org,
			URL:      issue.URL,
			Amount:   amount,
			Currency: "USD",
			Source:   "boss",
			IssueNum: issueNum,
		})
	}

	return bounties, nil
}

// parseBossHID extracts org and issue number from "owner/repo#123".
func parseBossHID(hid string) (org, issueNum string) {
	parts := strings.SplitN(hid, "#", 2)
	if len(parts) == 2 {
		org = parts[0]
		issueNum = "#" + parts[1]
	} else {
		org = hid
	}
	return org, issueNum
}
