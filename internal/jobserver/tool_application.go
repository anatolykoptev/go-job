package jobserver

import (
	"context"
	"errors"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerApplicationPrep(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "application_prep",
		Description: "Generate a complete application package in one call: ATS resume analysis, tailored cover letter, interview prep questions with model answers, and optional company research. Combines resume_analyze + cover_letter_generate + interview_prep into a single workflow.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.ApplicationPrepInput) (*mcp.CallToolResult, *jobs.ApplicationPrepResult, error) {
		if input.Resume == "" {
			return nil, nil, errors.New("resume is required")
		}
		if input.JobDescription == "" {
			return nil, nil, errors.New("job_description is required")
		}
		result, err := jobs.PrepareApplication(ctx, input.Resume, input.JobDescription, input.Company, input.Tone)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}
