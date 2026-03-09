package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// fetchIssueInfoBatch fetches title, state, and labels for each bounty in parallel.
// Returns a map[URL] → githubIssueInfo. On error for a given URL, defaults to open state.
func fetchIssueInfoBatch(ctx context.Context, bounties []engine.BountyListing) map[string]githubIssueInfo {
	result := make(map[string]githubIssueInfo, len(bounties))
	if len(bounties) == 0 || engine.Cfg.GithubToken == "" {
		// No token — return empty map (all bounties kept with defaults).
		return result
	}

	type kv struct {
		url  string
		info githubIssueInfo
	}
	ch := make(chan kv, len(bounties))
	var wg sync.WaitGroup

	for _, b := range bounties {
		owner, repo, number, ok := ParseGitHubIssueURL(b.URL)
		if !ok {
			ch <- kv{url: b.URL, info: githubIssueInfo{State: "open"}}
			continue
		}
		wg.Add(1)
		go func(url, owner, repo string, number int) {
			defer wg.Done()
			info := fetchSingleIssueInfo(ctx, owner, repo, number)
			ch <- kv{url: url, info: info}
		}(b.URL, owner, repo, number)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	for item := range ch {
		result[item.url] = item.info
	}

	// Fetch repo primary languages (one call per unique repo, not per issue).
	repoLangs := fetchRepoLanguages(ctx, bounties)
	for url, info := range result {
		owner, repo, _, ok := ParseGitHubIssueURL(url)
		if ok {
			if lang, found := repoLangs[owner+"/"+repo]; found {
				info.Language = lang
				result[url] = info
			}
		}
	}

	return result
}

// fetchSingleIssueInfo fetches title, state, and labels for a single GitHub issue.
func fetchSingleIssueInfo(ctx context.Context, owner, repo string, number int) githubIssueInfo {
	ctx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues/%d", owner, repo, number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return githubIssueInfo{State: "open"}
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", engine.UserAgentBot)
	req.Header.Set("Authorization", "Bearer "+engine.Cfg.GithubToken)

	resp, err := engine.Cfg.HTTPClient.Do(req) //nolint:gosec
	if err != nil {
		return githubIssueInfo{State: "open"}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return githubIssueInfo{State: "open"}
	}

	var issue struct {
		Title  string `json:"title"`
		State  string `json:"state"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&issue); err != nil {
		return githubIssueInfo{State: "open"}
	}

	labels := make([]string, len(issue.Labels))
	for i, l := range issue.Labels {
		labels[i] = l.Name
	}
	return githubIssueInfo{
		Title:  issue.Title,
		State:  issue.State,
		Labels: labels,
	}
}

// fetchRepoLanguages fetches the primary language for each unique repo in the bounty list.
// Returns map["owner/repo"] → "Go", "Rust", etc.
func fetchRepoLanguages(ctx context.Context, bounties []engine.BountyListing) map[string]string {
	// Collect unique repos.
	repos := make(map[string]bool)
	for _, b := range bounties {
		owner, repo, _, ok := ParseGitHubIssueURL(b.URL)
		if ok {
			repos[owner+"/"+repo] = true
		}
	}
	if len(repos) == 0 || engine.Cfg.GithubToken == "" {
		return nil
	}

	type kv struct {
		repo string
		lang string
	}
	ch := make(chan kv, len(repos))
	var wg sync.WaitGroup

	for repo := range repos {
		wg.Add(1)
		go func(repo string) {
			defer wg.Done()
			lang := fetchRepoLanguage(ctx, repo)
			if lang != "" {
				ch <- kv{repo: repo, lang: lang}
			}
		}(repo)
	}
	go func() {
		wg.Wait()
		close(ch)
	}()

	result := make(map[string]string)
	for item := range ch {
		result[item.repo] = item.lang
	}
	return result
}

// fetchRepoLanguage fetches the primary language for a GitHub repo.
func fetchRepoLanguage(ctx context.Context, repo string) string {
	ctx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	url := "https://api.github.com/repos/" + repo
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

	var r struct {
		Language string `json:"language"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return ""
	}
	return r.Language
}
