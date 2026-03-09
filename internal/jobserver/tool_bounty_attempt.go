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

func registerBountyAttempt(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "bounty_attempt",
		Description: "Claim a bounty by commenting /attempt on a GitHub issue. This signals to the maintainer that you are starting work on the bounty.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input engine.BountyAttemptInput) (*mcp.CallToolResult, engine.SmartSearchOutput, error) {
		owner, repo, number, ok := jobs.ParseGitHubIssueURL(input.URL)
		if !ok {
			return nil, engine.SmartSearchOutput{}, errors.New("invalid GitHub issue URL; expected https://github.com/org/repo/issues/123")
		}

		commentBody := "/attempt"
		commentURL, err := jobs.CommentOnIssue(ctx, owner, repo, number, commentBody)
		if err != nil {
			return nil, engine.SmartSearchOutput{}, fmt.Errorf("comment on issue: %w", err)
		}

		out := engine.BountyAttemptOutput{
			URL:        input.URL,
			CommentURL: commentURL,
			Status:     "attempted",
		}

		jsonBytes, err := json.Marshal(out)
		if err != nil {
			return nil, engine.SmartSearchOutput{}, errors.New("json marshal failed")
		}

		result := engine.SmartSearchOutput{
			Query:   input.URL,
			Answer:  string(jsonBytes),
			Sources: []engine.SourceItem{},
		}
		return nil, result, nil
	})
}
