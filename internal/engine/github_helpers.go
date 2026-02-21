package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// githubBlobRe matches github.com/:owner/:repo/blob/:ref/:path
var githubBlobRe = regexp.MustCompile(`^https?://github\.com/([^/]+/[^/]+)/blob/([^/]+)/(.+)$`)

// rawGitHubRe matches raw.githubusercontent.com/:owner/:repo/:ref/:path
var rawGitHubRe = regexp.MustCompile(`^https?://raw\.githubusercontent\.com/([^/]+)/([^/]+)/([^/]+)/(.+)$`)

// GithubRawURL converts a GitHub blob URL to raw.githubusercontent.com.
// Non-GitHub URLs are returned unchanged.
func GithubRawURL(u string) string {
	m := githubBlobRe.FindStringSubmatch(u)
	if m == nil {
		return u
	}
	return fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s", m[1], m[2], m[3])
}

// IsRawGitHubURL returns true for raw.githubusercontent.com URLs.
func IsRawGitHubURL(u string) bool {
	return rawGitHubRe.MatchString(u)
}

// searchRepoTree returns all blob paths in the repo whose basename matches filename.
func searchRepoTree(ctx context.Context, owner, repo, filename string) ([]string, error) {
	treeURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/trees/HEAD?recursive=1", owner, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, treeURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgentBot)
	if cfg.GithubToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.GithubToken)
	}
	resp, err := cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tree API: status %d", resp.StatusCode)
	}
	var tree struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tree); err != nil {
		return nil, err
	}
	var matches []string
	for _, item := range tree.Tree {
		if item.Type == "blob" {
			base := item.Path[strings.LastIndex(item.Path, "/")+1:]
			if base == filename {
				matches = append(matches, item.Path)
			}
		}
	}
	return matches, nil
}
