package jobserver

import (
	"context"
	"fmt"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerResumeGenerate(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "resume_generate",
		Description: "Generate an ATS-optimized resume tailored to a specific job description. Uses your master resume graph to select the most relevant experiences, projects, and achievements. Injects keywords from the JD for maximum ATS pass rate.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.ResumeGenerateInput) (*mcp.CallToolResult, *jobs.ResumeGenerateResult, error) {
		if input.JobDescription == "" {
			return nil, nil, fmt.Errorf("job_description is required")
		}
		result, err := jobs.GenerateResume(ctx, input.JobDescription, input.Company, input.Format)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}
