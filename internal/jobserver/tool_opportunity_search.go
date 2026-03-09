package jobserver

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerOpportunitySearch(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "opportunity_search",
		Description: "Search for income opportunities across all sources: code bounties (Algora, Opire, BountyHub, Boss, Lightning, Collaborators), security bug bounties (HackerOne, Bugcrowd, Intigriti, YesWeHack, Immunefi), and freelance jobs (RemoteOK, Himalayas). Filter by type and keyword.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.OpportunitySearchInput) (*mcp.CallToolResult, engine.SmartSearchOutput, error) {
		out, err := jobs.SearchOpportunities(ctx, input)
		if err != nil {
			return nil, engine.SmartSearchOutput{}, err
		}

		if len(out.Opportunities) == 0 {
			return nil, engine.SmartSearchOutput{
				Query:  input.Query,
				Answer: out.Summary,
			}, nil
		}

		jsonBytes, err := json.Marshal(out)
		if err != nil {
			return nil, engine.SmartSearchOutput{}, errors.New("json marshal failed")
		}

		return nil, engine.SmartSearchOutput{
			Query:   input.Query,
			Answer:  string(jsonBytes),
			Sources: []engine.SourceItem{},
		}, nil
	})
}
