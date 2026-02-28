package jobserver

import (
	"context"
	"errors"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerNegotiationPrep(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "negotiation_prep",
		Description: "Generate a salary negotiation playbook with market data, opening/closing scripts, talking points with anticipated counters, BATNA analysis, and red flags. Optionally enriches with salary research benchmarks.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.NegotiationPrepInput) (*mcp.CallToolResult, *jobs.NegotiationPrepResult, error) {
		if input.Role == "" {
			return nil, nil, errors.New("role is required")
		}
		if input.CurrentOffer == "" {
			return nil, nil, errors.New("current_offer is required")
		}
		result, err := jobs.PrepareNegotiation(ctx, input.Role, input.Company, input.Location, input.CurrentOffer, input.TargetComp, input.Leverage)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}
