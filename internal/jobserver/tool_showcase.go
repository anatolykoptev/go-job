package jobserver

import (
	"context"
	"errors"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerProjectShowcase(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "project_showcase",
		Description: "Transform project descriptions into STAR-format interview narratives with quantified impact and talking points. Helps you articulate projects compellingly in interviews.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.ProjectShowcaseInput) (*mcp.CallToolResult, *jobs.ProjectShowcaseResult, error) {
		if input.Projects == "" {
			return nil, nil, errors.New("projects is required")
		}
		result, err := jobs.ShowcaseProjects(ctx, input.Projects, input.TargetRole)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}
