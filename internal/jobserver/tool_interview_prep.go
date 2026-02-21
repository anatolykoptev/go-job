package jobserver

import (
	"context"
	"errors"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerInterviewPrep(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "interview_prep",
		Description: "Generate personalized interview questions with model answers based on your resume and the job description. Optionally enriches with company research for company-specific questions. Returns behavioral, technical, and system design Q&A with answers grounded in your actual projects.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.InterviewPrepInput) (*mcp.CallToolResult, *jobs.InterviewPrepResult, error) {
		if input.Resume == "" {
			return nil, nil, errors.New("resume is required")
		}
		if input.JobDescription == "" {
			return nil, nil, errors.New("job_description is required")
		}
		result, err := jobs.PrepareInterview(ctx, input.Resume, input.JobDescription, input.Company, input.Focus)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}
