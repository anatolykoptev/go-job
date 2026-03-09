package jobserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerOpportunityClaim(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "opportunity_claim",
		Description: "Claim an income opportunity. For code bounties: posts /attempt comment on the GitHub issue. For security programs: advises manual process. For freelance: advises using application_prep.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.OpportunityClaimInput) (*mcp.CallToolResult, engine.SmartSearchOutput, error) {
		if input.URL == "" {
			return nil, engine.SmartSearchOutput{}, errors.New("url is required")
		}

		typ := jobs.DetectOpportunityType(input.URL)
		if typ == "" {
			return nil, engine.SmartSearchOutput{}, errors.New("cannot detect opportunity type from URL")
		}

		var result map[string]string

		switch typ {
		case "bounty":
			owner, repo, number, ok := jobs.ParseGitHubIssueURL(input.URL)
			if !ok {
				return nil, engine.SmartSearchOutput{}, errors.New("invalid GitHub issue URL; expected https://github.com/org/repo/issues/123")
			}

			commentURL, err := jobs.CommentOnIssue(ctx, owner, repo, number, "/attempt")
			if err != nil {
				return nil, engine.SmartSearchOutput{}, fmt.Errorf("comment on issue: %w", err)
			}

			result = map[string]string{
				"type":        "bounty",
				"url":         input.URL,
				"comment_url": commentURL,
				"status":      "attempted",
				"message":     "Posted /attempt on the issue. You are now working on this bounty.",
			}

		case "security":
			result = map[string]string{
				"type":    "security",
				"url":     input.URL,
				"status":  "manual",
				"message": "Security bug bounty programs don't have an automated claim step. Visit the program page, read the rules and scope, then use security scanning tools to find vulnerabilities.",
			}

		case "freelance":
			result = map[string]string{
				"type":    "freelance",
				"url":     input.URL,
				"status":  "manual",
				"message": "Freelance projects require manual application. Use application_prep tool to prepare your application materials.",
			}
		}

		jsonBytes, err := json.Marshal(result)
		if err != nil {
			return nil, engine.SmartSearchOutput{}, errors.New("json marshal failed")
		}

		return nil, engine.SmartSearchOutput{
			Query:   input.URL,
			Answer:  string(jsonBytes),
			Sources: []engine.SourceItem{},
		}, nil
	})
}
