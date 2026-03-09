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

// FetchLinkedPRs finds pull requests that reference the given issue.
// Uses GitHub's search API: "type:pr repo:owner/repo <number> in:body,comments".
func FetchLinkedPRs(ctx context.Context, owner, repo string, number int) ([]engine.CompetingPR, error) {
	ctx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	// Search for PRs mentioning this issue number in the same repo.
	query := fmt.Sprintf("type:pr repo:%s/%s %d", owner, repo, number)
	url := fmt.Sprintf("https://api.github.com/search/issues?q=%s&per_page=10",
		strings.ReplaceAll(query, " ", "+"))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", engine.UserAgentBot)
	if engine.Cfg.GithubToken != "" {
		req.Header.Set("Authorization", "Bearer "+engine.Cfg.GithubToken)
	}

	resp, err := engine.Cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("github search returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Items []struct {
			Number      int    `json:"number"`
			Title       string `json:"title"`
			HTMLURL     string `json:"html_url"`
			State       string `json:"state"`
			PullRequest *struct {
				MergedAt *string `json:"merged_at"`
			} `json:"pull_request"`
			User struct {
				Login string `json:"login"`
			} `json:"user"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode search: %w", err)
	}

	var prs []engine.CompetingPR
	for _, item := range result.Items {
		if item.PullRequest == nil {
			continue // not a PR
		}
		state := item.State
		if item.PullRequest.MergedAt != nil {
			state = "merged"
		}
		prs = append(prs, engine.CompetingPR{
			Number: item.Number,
			Title:  item.Title,
			Author: item.User.Login,
			State:  state,
			URL:    item.HTMLURL,
		})
	}

	slog.Debug("github: linked PRs", slog.Int("issue", number), slog.Int("prs", len(prs)))
	return prs, nil
}
