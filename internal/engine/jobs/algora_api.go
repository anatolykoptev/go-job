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

	"github.com/anatolykoptev/go_job/internal/engine"
)

// Algora tRPC API — public endpoint, no auth needed.
const algoraTRPCURL = "https://console.algora.io/api/trpc/bounty.list"

// tRPC response wrapper.
type algoraTRPCResponse []struct {
	Result struct {
		Data struct {
			JSON struct {
				Items      []algoraAPIBounty `json:"items"`
				NextCursor *string           `json:"next_cursor"`
			} `json:"json"`
		} `json:"data"`
	} `json:"result"`
}

// algoraAPIBounty matches the tRPC bounty.list item structure.
type algoraAPIBounty struct {
	ID              string   `json:"id"`
	Status          string   `json:"status"`
	RewardFormatted string   `json:"reward_formatted"`
	Tech            []string `json:"tech"`
	CreatedAt       string   `json:"created_at"`
	Reward          struct {
		Currency string `json:"currency"`
		Amount   int    `json:"amount"`
	} `json:"reward"`
	Task struct {
		Title     string `json:"title"`
		URL       string `json:"url"`
		RepoName  string `json:"repo_name"`
		RepoOwner string `json:"repo_owner"`
		Number    int    `json:"number"`
		Forge     string `json:"forge"`
	} `json:"task"`
}

// searchAlgoraAPI fetches bounties from Algora's public tRPC API.
// Returns nil, nil to signal caller should try scraping fallback.
func searchAlgoraAPI(ctx context.Context, limit int) ([]engine.BountyListing, error) {
	engine.IncrAlgoraRequests()

	fetchCtx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	// Build tRPC query: input={"json":{"limit":N}}
	input := fmt.Sprintf(`{"json":{"limit":%d}}`, limit)
	reqURL := algoraTRPCURL + "?input=" + url.QueryEscape(input)

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := engine.RetryHTTP(fetchCtx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		return engine.Cfg.HTTPClient.Do(req) //nolint:gosec // intentional outbound HTTP request
	})
	if err != nil {
		return nil, fmt.Errorf("algora tRPC request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("algora tRPC returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, err
	}

	var trpcResp algoraTRPCResponse
	if err := json.Unmarshal(body, &trpcResp); err != nil {
		return nil, fmt.Errorf("algora tRPC JSON parse failed: %w", err)
	}

	if len(trpcResp) == 0 {
		return nil, fmt.Errorf("algora tRPC: empty response array")
	}

	items := trpcResp[0].Result.Data.JSON.Items
	bounties := make([]engine.BountyListing, 0, len(items))
	for _, item := range items {
		if item.Status != "open" {
			continue
		}
		ghURL := item.Task.URL
		if ghURL == "" {
			ghURL = buildGitHubURL(item.Task.Forge, item.Task.RepoOwner, item.Task.RepoName, item.Task.Number)
		}
		if ghURL == "" {
			continue
		}

		bounties = append(bounties, engine.BountyListing{
			Title:    item.Task.Title,
			Org:      item.Task.RepoOwner,
			URL:      ghURL,
			Amount:   item.RewardFormatted,
			Currency: item.Reward.Currency,
			Skills:   item.Tech,
			Source:   "algora",
			IssueNum: "#" + strconv.Itoa(item.Task.Number),
			Posted:   item.CreatedAt,
		})
	}

	slog.Debug("algora: tRPC API fetch complete", slog.Int("results", len(bounties)))
	return bounties, nil
}

// buildGitHubURL constructs a GitHub issue URL from API fields.
func buildGitHubURL(forge, owner, repo string, number int) string {
	if forge != "github" || owner == "" || repo == "" || number == 0 {
		return ""
	}
	return "https://github.com/" + owner + "/" + repo + "/issues/" + strconv.Itoa(number)
}
