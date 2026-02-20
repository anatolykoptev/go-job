package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// RepoMeta holds GitHub repository metadata from the REST API.
type RepoMeta struct {
	FullName      string   `json:"full_name"`
	Description   string   `json:"description"`
	Stars         int      `json:"stargazers_count"`
	Language      string   `json:"language"`
	Topics        []string `json:"topics"`
	DefaultBranch string   `json:"default_branch"`
	LastPush      string   `json:"pushed_at"`
	Archived      bool     `json:"archived"`
	HTMLURL       string   `json:"html_url"`
}

// ownerRepoRe matches github.com/:owner/:repo (with optional trailing path).
var ownerRepoRe = regexp.MustCompile(`(?i)github\.com/([A-Za-z0-9._-]+)/([A-Za-z0-9._-]+)`)

// ExtractOwnerRepo extracts owner and repo from any github.com URL.
func ExtractOwnerRepo(u string) (owner, repo string, ok bool) {
	m := ownerRepoRe.FindStringSubmatch(u)
	if m == nil {
		return "", "", false
	}
	repo = strings.TrimSuffix(m[2], ".git")
	// Skip non-repo GitHub pages (e.g. github.com/topics/golang, github.com/explore)
	for _, skip := range []string{"topics", "explore", "trending", "search", "settings", "notifications"} {
		if strings.EqualFold(m[1], skip) || strings.EqualFold(m[2], skip) {
			return "", "", false
		}
	}
	return m[1], repo, true
}

// FetchRepoMeta fetches repository metadata from GitHub REST API.
func FetchRepoMeta(ctx context.Context, owner, repo string) (*RepoMeta, error) {
	ctx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s", owner, repo)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", engine.UserAgentBot)
	if engine.Cfg.GithubToken != "" {
		req.Header.Set("Authorization", "Bearer "+engine.Cfg.GithubToken)
	}

	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		return engine.Cfg.HTTPClient.Do(req)
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("github API status %d for %s/%s", resp.StatusCode, owner, repo)
	}

	var meta RepoMeta
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

// FetchREADME fetches the README.md from a repository via raw.githubusercontent.com.
func FetchREADME(ctx context.Context, owner, repo, defaultBranch string) (string, error) {
	u := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/README.md", owner, repo, defaultBranch)
	return engine.FetchRawContent(ctx, u)
}

// ghRepoSearchResponse is the GitHub Repository Search API response.
type ghRepoSearchResponse struct {
	Items []RepoMeta `json:"items"`
}

// SearchGitHubRepos searches repositories via GitHub REST API.
// Supports full GitHub search syntax: language:go topic:ai stars:>100 user:owner
func SearchGitHubRepos(ctx context.Context, query, sort string) ([]engine.SearxngResult, error) {
	ctx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	params := url.Values{
		"q":        {query},
		"per_page": {"10"},
	}
	if sort != "" {
		params.Set("sort", sort)
	}

	apiURL := "https://api.github.com/search/repositories?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", engine.UserAgentBot)
	if engine.Cfg.GithubToken != "" {
		req.Header.Set("Authorization", "Bearer "+engine.Cfg.GithubToken)
	}

	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		return engine.Cfg.HTTPClient.Do(req)
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("github repo search API status %d", resp.StatusCode)
	}

	var data ghRepoSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	var results []engine.SearxngResult
	for _, item := range data.Items {
		if item.HTMLURL == "" {
			continue
		}
		title := fmt.Sprintf("%s (%s, %d stars)", item.FullName, item.Language, item.Stars)
		var parts []string
		if item.Description != "" {
			parts = append(parts, item.Description)
		}
		if len(item.Topics) > 0 {
			parts = append(parts, "Topics: "+strings.Join(item.Topics, ", "))
		}
		parts = append(parts, fmt.Sprintf("Stars: %d | Language: %s | Last push: %s | Archived: %v",
			item.Stars, item.Language, item.LastPush, item.Archived))
		results = append(results, engine.SearxngResult{
			Title:   title,
			URL:     item.HTMLURL,
			Content: strings.Join(parts, "\n"),
		})
	}
	return results, nil
}

// ghIssueSearchResponse is the GitHub Issues/PR Search API response.
type ghIssueSearchResponse struct {
	Items []ghIssueItem `json:"items"`
}

type ghIssueItem struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
	Body      string `json:"body"`
	Comments  int    `json:"comments"`
	CreatedAt string `json:"created_at"`
	Labels    []struct {
		Name string `json:"name"`
	} `json:"labels"`
	PullRequest *struct {
		MergedAt string `json:"merged_at"`
	} `json:"pull_request,omitempty"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// SearchGitHubIssues searches issues and pull requests via the GitHub Issues Search API.
// query should include "is:pr" or "is:issue" and optionally "repo:owner/repo".
func SearchGitHubIssues(ctx context.Context, query string) ([]engine.IssueItem, error) {
	ctx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	apiURL := "https://api.github.com/search/issues?" + url.Values{
		"q":        {query},
		"per_page": {"10"},
	}.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", engine.UserAgentBot)
	if engine.Cfg.GithubToken != "" {
		req.Header.Set("Authorization", "Bearer "+engine.Cfg.GithubToken)
	}

	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		return engine.Cfg.HTTPClient.Do(req)
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("github issues search API status %d", resp.StatusCode)
	}

	var data ghIssueSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	items := make([]engine.IssueItem, 0, len(data.Items))
	for _, item := range data.Items {
		var labels []string
		for _, l := range item.Labels {
			labels = append(labels, l.Name)
		}

		body := strings.TrimSpace(item.Body)
		if len(body) > 500 {
			body = body[:500] + "..."
		}

		mergedAt := ""
		if item.PullRequest != nil {
			mergedAt = item.PullRequest.MergedAt
		}

		items = append(items, engine.IssueItem{
			Number:    item.Number,
			Title:     item.Title,
			URL:       item.HTMLURL,
			State:     item.State,
			Author:    item.User.Login,
			Labels:    labels,
			Body:      body,
			Comments:  item.Comments,
			CreatedAt: item.CreatedAt,
			MergedAt:  mergedAt,
			Repo:      item.Repository.FullName,
		})
	}
	return items, nil
}

// ghCodeSearchResponse is the GitHub Code Search API response.
type ghCodeSearchResponse struct {
	Items []ghCodeSearchItem `json:"items"`
}

type ghCodeSearchItem struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	HTMLURL    string `json:"html_url"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	TextMatches []struct {
		Fragment string `json:"fragment"`
	} `json:"text_matches"`
}

// SearchGitHubCode searches code within the given repos using the GitHub Code Search API.
func SearchGitHubCode(ctx context.Context, query string, repos []string) ([]engine.SearxngResult, error) {
	ctx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	// Build query: "search terms repo:owner/repo1 repo:owner/repo2"
	q := query
	for _, r := range repos {
		q += " repo:" + r
	}

	apiURL := "https://api.github.com/search/code?" + url.Values{
		"q":        {q},
		"per_page": {"10"},
	}.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3.text-match+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", engine.UserAgentBot)
	if engine.Cfg.GithubToken != "" {
		req.Header.Set("Authorization", "Bearer "+engine.Cfg.GithubToken)
	}

	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		return engine.Cfg.HTTPClient.Do(req)
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("github code search API status %d", resp.StatusCode)
	}

	var data ghCodeSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	var results []engine.SearxngResult
	for _, item := range data.Items {
		if item.HTMLURL == "" {
			continue
		}

		// Title: "filename — repo/path"
		title := fmt.Sprintf("%s — %s/%s", item.Name, item.Repository.FullName, item.Path)
		if len(title) > 120 {
			pathParts := strings.Split(item.Path, "/")
			if len(pathParts) > 2 {
				title = fmt.Sprintf("%s — .../%s", item.Name, strings.Join(pathParts[len(pathParts)-2:], "/"))
			}
		}

		// Content: text match fragments joined
		var fragments []string
		for _, tm := range item.TextMatches {
			frag := strings.TrimSpace(tm.Fragment)
			if frag == "" {
				continue
			}
			if len(frag) > 300 {
				frag = frag[:297] + "..."
			}
			fragments = append(fragments, frag)
		}
		content := strings.Join(fragments, " | ")
		if content == "" {
			content = "File: " + item.Path
		}
		if len(content) > 600 {
			content = content[:597] + "..."
		}

		results = append(results, engine.SearxngResult{
			Title:   title,
			URL:     item.HTMLURL,
			Content: content,
		})
	}
	return results, nil
}
