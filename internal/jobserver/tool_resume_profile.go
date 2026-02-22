package jobserver

import (
	"context"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerResumeProfile(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "resume_profile",
		Description: "Read the stored resume profile from the database. Returns structured data: personal info, experiences, skills, projects, achievements, educations, certifications, domains, methodologies. Optionally filter by section. Use this to see what the user's resume contains before generating tailored versions.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.ResumeProfileInput) (*mcp.CallToolResult, *jobs.ResumeProfileResult, error) {
		result, err := jobs.GetResumeProfile(ctx, input.Section)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}
