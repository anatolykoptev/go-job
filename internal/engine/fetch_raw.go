package engine

import (
	"context"
	"fmt"
	"strings"
)

// FetchRawContent fetches a URL as plain text (no readability extraction).
// Used for raw.githubusercontent.com and similar plain-text endpoints.
func FetchRawContent(ctx context.Context, rawURL string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.FetchTimeout)
	defer cancel()

	resp, err := fetchWithRetry(ctx, rawURL, false)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := readResponseBody(resp)
	if err != nil {
		return "", err
	}

	text := strings.TrimSpace(string(body))
	if len(text) > cfg.MaxContentChars {
		text = text[:cfg.MaxContentChars] + "..."
	}
	return text, nil
}

// FetchRawContentWithFallback fetches a raw.githubusercontent.com URL.
// On 404 it tries: (1) alt branch (main↔master), (2) GitHub tree search by filename.
// On success with a moved file, transparently returns the new content.
// On failure, includes the suggested correct URL(s) in the error message.
func FetchRawContentWithFallback(ctx context.Context, rawURL string) (string, error) {
	content, err := FetchRawContent(ctx, rawURL)
	if err == nil {
		return content, nil
	}
	if !strings.Contains(err.Error(), "404") {
		return "", err
	}

	m := rawGitHubRe.FindStringSubmatch(rawURL)
	if m == nil {
		return "", err
	}
	owner, repo, ref, filePath := m[1], m[2], m[3], m[4]

	// 1. Try alternative branch (main ↔ master)
	altRef := map[string]string{"main": "master", "master": "main"}[ref]
	if altRef != "" {
		altURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s", owner, repo, altRef, filePath)
		if c, altErr := FetchRawContent(ctx, altURL); altErr == nil {
			return c, nil
		}
	}

	// 2. Search repo tree for a file with the same name
	filename := filePath[strings.LastIndex(filePath, "/")+1:]
	matches, searchErr := searchRepoTree(ctx, owner, repo, filename)
	if searchErr != nil || len(matches) == 0 {
		return "", fmt.Errorf("%w (file may have moved; could not locate it in repo tree)", err)
	}

	// Try fetching the first match
	firstURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/HEAD/%s", owner, repo, matches[0])
	if c, fetchErr := FetchRawContent(ctx, firstURL); fetchErr == nil {
		return c, nil
	}

	// Return suggestions in the error
	var suggestions []string
	for _, p := range matches {
		suggestions = append(suggestions, fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/HEAD/%s", owner, repo, p))
	}
	return "", fmt.Errorf("%w (file moved; try: %s)", err, strings.Join(suggestions, " or "))
}
