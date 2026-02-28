package jobserver

import (
	"context"
	"errors"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerOfferCompare(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "offer_compare",
		Description: "Compare multiple job offers side-by-side across compensation, benefits, work-life balance, growth potential, and stability. Scores each offer 0-100 and recommends the best choice based on your priorities.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.OfferCompareInput) (*mcp.CallToolResult, *jobs.OfferCompareResult, error) {
		if input.Offers == "" {
			return nil, nil, errors.New("offers is required")
		}
		result, err := jobs.CompareOffers(ctx, input.Offers, input.Priorities)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}
