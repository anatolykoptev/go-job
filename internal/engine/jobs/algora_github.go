package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// githubIssueState is a minimal response from the GitHub Issues API.
type githubIssueState struct {
	State string `json:"state"`
}

// githubIssueInfo holds enrichment data from a single GitHub API call.
type githubIssueInfo struct {
	Title    string   // issue title
	State    string   // "open" or "closed"
	Labels   []string // label names
	Language string   // repo primary language (from fetchRepoLanguages)
}

// FilterOpenBounties checks GitHub issue status in parallel and returns only open issues.
func FilterOpenBounties(ctx context.Context, bounties []engine.BountyListing) []engine.BountyListing {
	if len(bounties) == 0 || engine.Cfg.GithubToken == "" {
		return bounties // no token — skip filtering, return all
	}

	type result struct {
		idx  int
		open bool
	}

	results := make(chan result, len(bounties))
	var wg sync.WaitGroup

	for i, b := range bounties {
		owner, repo, number, ok := ParseGitHubIssueURL(b.URL)
		if !ok {
			results <- result{idx: i, open: true} // can't parse — keep it
			continue
		}

		wg.Add(1)
		go func(idx int, owner, repo string, number int) {
			defer wg.Done()
			open := checkIssueOpen(ctx, owner, repo, number)
			results <- result{idx: idx, open: open}
		}(i, owner, repo, number)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	openSet := make(map[int]bool, len(bounties))
	for r := range results {
		if r.open {
			openSet[r.idx] = true
		}
	}

	filtered := make([]engine.BountyListing, 0, len(openSet))
	for i, b := range bounties {
		if openSet[i] {
			filtered = append(filtered, b)
		}
	}

	if dropped := len(bounties) - len(filtered); dropped > 0 {
		slog.Debug("algora: filtered closed bounties", slog.Int("dropped", dropped), slog.Int("remaining", len(filtered)))
	}
	return filtered
}

// checkIssueOpen returns true if the GitHub issue is open (or if we can't determine status).
func checkIssueOpen(ctx context.Context, owner, repo string, number int) bool {
	ctx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues/%d", owner, repo, number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return true // error — keep bounty
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", engine.UserAgentBot)
	if engine.Cfg.GithubToken != "" {
		req.Header.Set("Authorization", "Bearer "+engine.Cfg.GithubToken)
	}

	resp, err := engine.Cfg.HTTPClient.Do(req) //nolint:gosec // GitHub API URL
	if err != nil {
		return true // error — keep bounty
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return true // can't determine — keep bounty
	}

	var issue githubIssueState
	if err := json.NewDecoder(resp.Body).Decode(&issue); err != nil {
		return true
	}

	return issue.State == "open"
}

// ParseGitHubIssueURL extracts owner, repo, issue number from a GitHub issue URL.
func ParseGitHubIssueURL(u string) (owner, repo string, number int, ok bool) {
	const prefix = "https://github.com/"
	if !strings.HasPrefix(u, prefix) {
		return "", "", 0, false
	}
	path := strings.TrimPrefix(u, prefix)
	parts := strings.Split(path, "/")
	// expect: owner/repo/issues/123
	if len(parts) < 4 || parts[2] != "issues" {
		return "", "", 0, false
	}
	n, err := strconv.Atoi(parts[3])
	if err != nil {
		return "", "", 0, false
	}
	return parts[0], parts[1], n, true
}

// FetchGitHubIssueTitle fetches the issue title from GitHub API.
// Returns empty string on any failure (graceful degradation).
func FetchGitHubIssueTitle(ctx context.Context, owner, repo string, number int) string {
	if engine.Cfg.GithubToken == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues/%d", owner, repo, number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", engine.UserAgentBot)
	req.Header.Set("Authorization", "Bearer "+engine.Cfg.GithubToken)

	resp, err := engine.Cfg.HTTPClient.Do(req) //nolint:gosec
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ""
	}

	var issue struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&issue); err != nil {
		return ""
	}
	return issue.Title
}

// ParseAmountCents parses "$4,000" → 400000 (cents) for sorting.
func ParseAmountCents(amount string) int {
	s := strings.ReplaceAll(amount, "$", "")
	s = strings.ReplaceAll(s, ",", "")
	s = strings.TrimSpace(s)
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n * 100
}
