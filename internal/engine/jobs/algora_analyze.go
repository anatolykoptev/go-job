package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// FetchGitHubIssueBody fetches the issue body from GitHub API.
func FetchGitHubIssueBody(ctx context.Context, owner, repo string, number int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues/%d", owner, repo, number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", engine.UserAgentBot)
	if engine.Cfg.GithubToken != "" {
		req.Header.Set("Authorization", "Bearer "+engine.Cfg.GithubToken)
	}

	resp, err := engine.Cfg.HTTPClient.Do(req) //nolint:gosec
	if err != nil {
		return "", fmt.Errorf("fetch issue: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github API returned %d", resp.StatusCode)
	}

	var issue struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&issue); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	return issue.Body, nil
}

// AnalyzeBounty fetches a GitHub issue and uses LLM to estimate complexity.
func AnalyzeBounty(ctx context.Context, issueURL string) (*engine.BountyAnalysis, error) {
	owner, repo, number, ok := ParseGitHubIssueURL(issueURL)
	if !ok {
		return nil, fmt.Errorf("invalid GitHub issue URL: %s", issueURL)
	}

	// Try to find bounty title+amount from cached enriched data.
	var title, amount string
	enriched, err := SearchAlgoraEnriched(ctx, 50)
	if err == nil {
		for _, bv := range enriched {
			if bv.Bounty.URL == issueURL {
				title = bv.Bounty.Title
				amount = bv.Bounty.Amount
				break
			}
		}
	}

	// Fetch issue body from GitHub.
	body, err := FetchGitHubIssueBody(ctx, owner, repo, number)
	if err != nil {
		return nil, fmt.Errorf("fetch issue body: %w", err)
	}

	// Fetch competing PRs.
	competingPRs, prErr := FetchLinkedPRs(ctx, owner, repo, number)
	if prErr != nil {
		slog.Warn("analyze: failed to fetch linked PRs", slog.Any("error", prErr))
	}

	// Truncate body to avoid exceeding LLM context.
	const maxBodyLen = 6000
	if len(body) > maxBodyLen {
		body = body[:maxBodyLen] + "\n...(truncated)"
	}

	// Build LLM prompt.
	prompt := buildAnalyzePrompt(title, amount, owner, repo, body, competingPRs)

	// Call LLM.
	raw, err := engine.CallLLM(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("LLM call: %w", err)
	}

	// Parse JSON from LLM response.
	analysis, err := parseAnalysisJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("parse LLM response: %w", err)
	}

	// Override title and amount from enriched data if available.
	if title != "" {
		analysis.Title = title
	}
	if amount != "" {
		analysis.Amount = amount
	}
	analysis.CompetingPRs = competingPRs

	return analysis, nil
}

func buildAnalyzePrompt(title, amount, owner, repo, body string, prs []engine.CompetingPR) string {
	var sb strings.Builder
	sb.WriteString("Analyze this GitHub bounty issue and estimate the effort required.\n\n")
	if title != "" {
		sb.WriteString("Title: " + title + "\n")
	}
	if amount != "" {
		sb.WriteString("Bounty amount: " + amount + "\n")
	}
	sb.WriteString("Repository: " + owner + "/" + repo + "\n\n")
	sb.WriteString("Issue body:\n" + body + "\n\n")

	if len(prs) > 0 {
		sb.WriteString("COMPETING PULL REQUESTS (already submitted for this issue):\n")
		for _, pr := range prs {
			sb.WriteString(fmt.Sprintf("  - PR #%d by @%s [%s]: %s\n", pr.Number, pr.Author, pr.State, pr.Title))
		}
		sb.WriteString("\n")
	}

	sb.WriteString(`Respond with ONLY a JSON object (no markdown, no explanation):
{
  "complexity": <1-5 integer>,
  "est_hours": "<range like 2-4 hours>",
  "dollar_per_hr": "<effective $/hr range like $50-100/hr>",
  "skills_needed": ["skill1", "skill2"],
  "summary": "<1-2 sentence summary of what needs to be done>",
  "verdict": "<recommended|fair|avoid>"
}

Complexity scale: 1=trivial fix, 2=small task, 3=moderate feature, 4=significant work, 5=major effort.
Verdict: "recommended" if $/hr > $75, "fair" if $25-75/hr, "avoid" if < $25/hr or very high risk.`)

	if len(prs) > 0 {
		sb.WriteString(`
IMPORTANT: There are already competing PRs for this bounty. If any PR is open or merged, set verdict to "avoid" and note the competition in the summary.`)
	}

	return sb.String()
}

func parseAnalysisJSON(raw string) (*engine.BountyAnalysis, error) {
	// Strip markdown code fences if present.
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s[3:], "\n"); idx >= 0 {
			s = s[3+idx+1:]
		}
		if idx := strings.LastIndex(s, "```"); idx >= 0 {
			s = s[:idx]
		}
		s = strings.TrimSpace(s)
	}

	var analysis engine.BountyAnalysis
	if err := json.Unmarshal([]byte(s), &analysis); err != nil {
		slog.Warn("algora: failed to parse LLM analysis JSON", slog.String("raw", raw), slog.Any("error", err))
		return nil, fmt.Errorf("invalid JSON from LLM: %w", err)
	}
	return &analysis, nil
}
