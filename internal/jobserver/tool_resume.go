package jobserver

import (
	"context"
	"errors"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerResumeAnalyze(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "resume_analyze",
		Description: "Analyze a resume against a job description. Returns ATS score (0-100), matching/missing keywords, experience gaps, and specific recommendations to improve match rate.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.ResumeAnalyzeInput) (*mcp.CallToolResult, *jobs.ResumeAnalysisResult, error) {
		if input.Resume == "" {
			return nil, nil, errors.New("resume is required")
		}
		if input.JobDescription == "" {
			return nil, nil, errors.New("job_description is required")
		}
		result, err := jobs.AnalyzeResume(ctx, input.Resume, input.JobDescription)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}

func registerCoverLetterGenerate(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "cover_letter_generate",
		Description: "Generate a tailored cover letter from a resume and job description. Tone options: professional (default), friendly, concise. Returns the cover letter text with word count.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.CoverLetterInput) (*mcp.CallToolResult, *jobs.CoverLetterResult, error) {
		if input.Resume == "" {
			return nil, nil, errors.New("resume is required")
		}
		if input.JobDescription == "" {
			return nil, nil, errors.New("job_description is required")
		}
		result, err := jobs.GenerateCoverLetter(ctx, input.Resume, input.JobDescription, input.Tone)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}

func registerResumeTailor(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "resume_tailor",
		Description: "Rewrite resume sections to better match a specific job description. Incorporates missing keywords naturally, reorders bullet points by relevance, quantifies achievements. Returns tailored resume + diff summary.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.ResumeTailorInput) (*mcp.CallToolResult, *jobs.ResumeTailorResult, error) {
		if input.Resume == "" {
			return nil, nil, errors.New("resume is required")
		}
		if input.JobDescription == "" {
			return nil, nil, errors.New("job_description is required")
		}
		result, err := jobs.TailorResume(ctx, input.Resume, input.JobDescription)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}
