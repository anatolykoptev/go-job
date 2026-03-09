package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// commentRequest is the JSON body for the GitHub create-comment API.
type commentRequest struct {
	Body string `json:"body"`
}

// commentResponse is a minimal GitHub comment response.
type commentResponse struct {
	HTMLURL string `json:"html_url"`
}

// CommentOnIssue posts a comment on a GitHub issue and returns the comment HTML URL.
func CommentOnIssue(ctx context.Context, owner, repo string, number int, body string) (string, error) {
	if engine.Cfg.GithubToken == "" {
		return "", fmt.Errorf("GITHUB_TOKEN is not set; cannot comment on issues")
	}

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues/%d/comments", owner, repo, number)

	payload, err := json.Marshal(commentRequest{Body: body})
	if err != nil {
		return "", fmt.Errorf("marshal comment body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", engine.UserAgentBot)
	req.Header.Set("Authorization", "Bearer "+engine.Cfg.GithubToken)

	resp, err := engine.Cfg.HTTPClient.Do(req) //nolint:gosec // GitHub API URL
	if err != nil {
		return "", fmt.Errorf("post comment: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("github comment failed (status %d): %s", resp.StatusCode, errBody)
	}

	var cr commentResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	return cr.HTMLURL, nil
}
