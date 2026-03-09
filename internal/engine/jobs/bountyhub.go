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
	bountyHubAPIURL         = "https://api.bountyhub.dev/api/bounties"
	bountyHubScrapeCacheKey = "bountyhub_scrape"
)

type bountyHubResponse struct {
	Data        []bountyHubItem `json:"data"`
	HasNextPage bool            `json:"hasNextPage"`
}

type bountyHubItem struct {
	ID                 string `json:"id"`
	Title              string `json:"title"`
	HtmlURL            string `json:"htmlURL"`
	Language           string `json:"language"`
	RepositoryFullName string `json:"repositoryFullName"`
	IssueNumber        int    `json:"issueNumber"`
	IssueState         string `json:"issueState"`
	TotalAmount        string `json:"totalAmount"`
	Solved             bool   `json:"solved"`
	Claimed            bool   `json:"claimed"`
	CreatedAt          string `json:"createdAt"`
}

// SearchBountyHub fetches open bounties from BountyHub. Results are cached.
func SearchBountyHub(ctx context.Context, limit int) ([]engine.BountyListing, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	if cached, ok := engine.CacheLoadJSON[[]engine.BountyListing](ctx, bountyHubScrapeCacheKey); ok {
		slog.Debug("bountyhub: using cached results", slog.Int("results", len(cached)))
		if len(cached) > limit {
			cached = cached[:limit]
		}
		return cached, nil
	}

	bounties, err := fetchBountyHub(ctx)
	if err != nil {
		return nil, err
	}

	engine.CacheStoreJSON(ctx, bountyHubScrapeCacheKey, "", bounties)
	if len(bounties) > limit {
		bounties = bounties[:limit]
	}

	slog.Debug("bountyhub: fetch complete", slog.Int("results", len(bounties)))
	return bounties, nil
}

func fetchBountyHub(ctx context.Context) ([]engine.BountyListing, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	url := bountyHubAPIURL + `?page=1&limit=50&filters={"solved":false}&sort=[{"orderBy":"totalAmount","order":"desc"}]`

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentChrome)

	resp, err := engine.Cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bountyhub request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bountyhub returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, err
	}

	bounties, _, err := parseBountyHubResponse(body)
	return bounties, err
}

func parseBountyHubResponse(data []byte) ([]engine.BountyListing, bool, error) {
	var resp bountyHubResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, false, fmt.Errorf("bountyhub: JSON parse failed: %w", err)
	}

	bounties := make([]engine.BountyListing, 0, len(resp.Data))
	for _, item := range resp.Data {
		if item.Solved || item.HtmlURL == "" {
			continue
		}

		amount := formatBountyHubAmount(item.TotalAmount)

		var skills []string
		if item.Language != "" {
			skills = []string{item.Language}
		}

		bounties = append(bounties, engine.BountyListing{
			Title:    item.Title,
			Org:      item.RepositoryFullName,
			URL:      item.HtmlURL,
			Amount:   amount,
			Currency: "USD",
			Skills:   skills,
			Source:   "bountyhub",
			IssueNum: "#" + strconv.Itoa(item.IssueNumber),
			Posted:   item.CreatedAt,
		})
	}

	return bounties, resp.HasNextPage, nil
}

// formatBountyHubAmount converts "500.00" to "$500", "1234.56" to "$1,234".
func formatBountyHubAmount(s string) string {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 {
		return "$0"
	}
	dollars := int(f)
	return formatCentsUSD(dollars * 100)
}
