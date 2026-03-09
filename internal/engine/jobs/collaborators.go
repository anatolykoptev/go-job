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
	collaboratorsAPIURL         = "https://collaborators.build/api/bounties"
	collaboratorsScrapeCacheKey = "collaborators_scrape"
)

type collaboratorsBounty struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	BountyAmount    string   `json:"bountyAmount"`
	Status          string   `json:"status"`
	IsSolved        bool     `json:"isSolved"`
	GithubIssueURL  string   `json:"githubIssueUrl"`
	GithubRepoOwner string   `json:"githubRepoOwner"`
	GithubRepoName  string   `json:"githubRepoName"`
	GithubIssueID   int      `json:"githubIssueId"`
	GithubLabels    []string `json:"githubLabels"`
	CreatedAt       string   `json:"createdAt"`
}

// SearchCollaborators fetches open bounties from Collaborators.build.
func SearchCollaborators(ctx context.Context, limit int) ([]engine.BountyListing, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	if cached, ok := engine.CacheLoadJSON[[]engine.BountyListing](ctx, collaboratorsScrapeCacheKey); ok {
		slog.Debug("collaborators: using cached results", slog.Int("results", len(cached)))
		if len(cached) > limit {
			cached = cached[:limit]
		}
		return cached, nil
	}

	bounties, err := fetchCollaborators(ctx)
	if err != nil {
		return nil, err
	}

	engine.CacheStoreJSON(ctx, collaboratorsScrapeCacheKey, "", bounties)
	if len(bounties) > limit {
		bounties = bounties[:limit]
	}

	slog.Debug("collaborators: fetch complete", slog.Int("results", len(bounties)))
	return bounties, nil
}

func fetchCollaborators(ctx context.Context) ([]engine.BountyListing, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, collaboratorsAPIURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentChrome)

	resp, err := engine.Cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("collaborators request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("collaborators returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, err
	}

	return parseCollaboratorsResponse(body)
}

func parseCollaboratorsResponse(data []byte) ([]engine.BountyListing, error) {
	var items []collaboratorsBounty
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("collaborators: JSON parse failed: %w", err)
	}

	bounties := make([]engine.BountyListing, 0, len(items))
	for _, item := range items {
		if item.Status != "ACTIVE" || item.IsSolved {
			continue
		}

		dollars, _ := strconv.Atoi(item.BountyAmount)
		amount := formatCentsUSD(dollars * 100)
		org := item.GithubRepoOwner + "/" + item.GithubRepoName
		issueNum := "#" + strconv.Itoa(item.GithubIssueID)

		bounties = append(bounties, engine.BountyListing{
			Title:    item.Title,
			Org:      org,
			URL:      item.GithubIssueURL,
			Amount:   amount,
			Currency: "USDC",
			Source:   "collaborators",
			IssueNum: issueNum,
		})
	}

	return bounties, nil
}
