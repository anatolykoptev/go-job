package jobserver

import (
	"context"
	"errors"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerSkillGap(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "skill_gap",
		Description: "Analyze skill gaps between your resume and a target job description. Returns match score, matching skills, missing skills with priority and learning time estimates, and a prioritized learning plan.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.SkillGapInput) (*mcp.CallToolResult, *jobs.SkillGapResult, error) {
		if input.Resume == "" {
			return nil, nil, errors.New("resume is required")
		}
		if input.JobDescription == "" {
			return nil, nil, errors.New("job_description is required")
		}
		result, err := jobs.AnalyzeSkillGap(ctx, input.Resume, input.JobDescription)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}
