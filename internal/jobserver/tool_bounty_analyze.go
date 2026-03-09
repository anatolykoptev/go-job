package jobserver

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerBountyAnalyze(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "bounty_analyze",
		Description: "Analyze a bounty's complexity, estimate hours, $/hr rate, and whether it's worth taking. Fetches the GitHub issue body and uses AI to assess effort vs reward.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.BountyAnalyzeInput) (*mcp.CallToolResult, engine.SmartSearchOutput, error) {
		if input.URL == "" {
			return nil, engine.SmartSearchOutput{}, errors.New("url is required")
		}

		analysis, err := jobs.AnalyzeBounty(ctx, input.URL)
		if err != nil {
			return nil, engine.SmartSearchOutput{}, err
		}

		jsonBytes, err := json.Marshal(analysis)
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
