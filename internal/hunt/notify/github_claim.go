package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/anatolykoptev/go_job/internal/hunt"
)

// GitHubClaimChecker checks whether a GitHub issue bounty is already claimed.
// "Claimed" is defined as: the issue has at least one assignee (GitHub issues)
// OR the Algora raw payload contains a truthy claim/attempt signal.
//
// Implements ClaimChecker. Safe for concurrent use.
type GitHubClaimChecker struct {
	// HTTPClient is used for GitHub API calls. If nil, http.DefaultClient is used.
	HTTPClient *http.Client
	token      string
}

// NewGitHubClaimChecker returns a GitHubClaimChecker that authenticates with token.
// Pass an empty token to make unauthenticated requests (lower rate limit: 60 req/h).
func NewGitHubClaimChecker(token string) *GitHubClaimChecker {
	return &GitHubClaimChecker{token: token}
}

// ghIssueResp is the minimal subset of the GitHub Issues API response we need.
type ghIssueResp struct {
	Assignees []struct{} `json:"assignees"`
}

// algoraRaw is the subset of Algora's raw payload we parse for claim signals.
type algoraRaw struct {
	AttemptsCount int  `json:"attempts_count"`
	Claimed       bool `json:"claimed"`
}

// ghIssueRE matches github.com/{owner}/{repo}/issues/{number}
var ghIssueRE = regexp.MustCompile(`github\.com/([^/]+)/([^/]+)/issues/(\d+)`)

// IsClaimed returns true when the bounty is already assigned / claimed.
//
// Decision logic:
//  1. If b.IssueNumber == 0, return false (not a GH issue — cannot determine claim state).
//  2. Parse owner/repo from b.URL. If unparseable, return false (fail-open).
//  3. If b.Source contains "algora", also check b.Raw for Algora claim signals.
//  4. Call GitHub Issues API; claimed = len(assignees) > 0.
func (c *GitHubClaimChecker) IsClaimed(ctx context.Context, b hunt.Bounty) (bool, error) {
	if b.IssueNumber == 0 {
		return false, nil
	}

	// Algora-specific check via raw payload (before network hit)
	if strings.Contains(strings.ToLower(b.Source), "algora") && len(b.Raw) > 0 {
		var ar algoraRaw
		if err := json.Unmarshal(b.Raw, &ar); err == nil {
			if ar.Claimed || ar.AttemptsCount > 0 {
				return true, nil
			}
		}
		// If unmarshal failed or fields absent → fall through to GH API
	}

	// Parse owner/repo from URL
	owner, repo, issueNum, ok := parseGitHubIssueURL(b.URL)
	if !ok {
		// Fallback: try constructing from IssueNumber alone — can't, need owner/repo
		return false, nil
	}

	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues/%s", owner, repo, issueNum)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return false, fmt.Errorf("github claim check: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("github claim check: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return false, nil // issue deleted / private → not claimable
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("github claim check: unexpected status %d for %s", resp.StatusCode, apiURL)
	}

	var issue ghIssueResp
	if err := json.NewDecoder(resp.Body).Decode(&issue); err != nil {
		return false, fmt.Errorf("github claim check: decode: %w", err)
	}

	return len(issue.Assignees) > 0, nil
}

// parseGitHubIssueURL extracts owner, repo, and issue number string from a
// GitHub issue URL. Returns ok=false if the URL does not match the expected pattern.
func parseGitHubIssueURL(rawURL string) (owner, repo, issueNum string, ok bool) {
	m := ghIssueRE.FindStringSubmatch(rawURL)
	if m == nil {
		return "", "", "", false
	}
	// Validate issue number is a positive integer
	n, err := strconv.Atoi(m[3])
	if err != nil || n <= 0 {
		return "", "", "", false
	}
	return m[1], m[2], m[3], true
}
