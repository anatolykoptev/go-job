package jobserver

import (
	"context"
	"errors"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerPitchGenerate(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "pitch_generate",
		Description: "Generate personalized elevator pitches (30-second and 2-minute) for a target role based on your resume. Optionally enriches with company research for a tailored 'Why this company?' answer.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.PitchGenerateInput) (*mcp.CallToolResult, *jobs.PitchGenerateResult, error) {
		if input.Resume == "" {
			return nil, nil, errors.New("resume is required")
		}
		if input.TargetRole == "" {
			return nil, nil, errors.New("target_role is required")
		}
		result, err := jobs.GeneratePitch(ctx, input.Resume, input.TargetRole, input.Company)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}
