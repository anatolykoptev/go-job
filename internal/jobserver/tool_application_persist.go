package jobserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/anatolykoptev/go_job/internal/engine/jobs/applications"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type applicationPersistInput struct {
	JobID    int64  `json:"job_id"    jsonschema:"Required. hunt_jobs.id — the job this application is for."`
	ResumeMD string `json:"resume_md" jsonschema:"Required. ATS-optimised resume in Markdown."`
	CoverMD  string `json:"cover_md"  jsonschema:"Required. Cover letter in Markdown."`
}

type applicationPersistResult struct {
	JobID       int64  `json:"job_id"`
	ResumePath  string `json:"resume_path,omitempty"`
	CoverPath   string `json:"cover_path,omitempty"`
	PDFRendered bool   `json:"pdf_rendered"`
	Message     string `json:"message"`
}

// registerApplicationPersist registers the application_persist MCP tool.
//
// This is intentionally a separate tool from resume_generate / cover_letter_generate
// (which are ReadOnlyHint:true and return text). A tool advertising ReadOnlyHint
// must not write; application_persist is the explicit command that writes.
func registerApplicationPersist(server *mcp.Server, authority *applications.Authority) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "application_persist",
		Description: "Persist a prepared resume + cover letter to the uploads store " +
			"(keyed by job_id). Renders PDF via Typst when pandoc+typst are available; " +
			"falls back to markdown-only storage otherwise. " +
			"Use after resume_generate + cover_letter_generate have produced the md drafts.",
		// No ReadOnlyHint — this tool writes files to uploads.
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input applicationPersistInput) (*mcp.CallToolResult, *applicationPersistResult, error) {
		if input.JobID <= 0 {
			return nil, nil, errors.New("job_id must be a positive hunt_jobs.id")
		}
		if input.ResumeMD == "" {
			return nil, nil, errors.New("resume_md is required")
		}
		if input.CoverMD == "" {
			return nil, nil, errors.New("cover_md is required")
		}

		res, err := authority.Persist(ctx, input.JobID, input.ResumeMD, input.CoverMD)
		if err != nil {
			return nil, nil, fmt.Errorf("application_persist: %w", err)
		}

		msg := "markdown persisted"
		if res.PDFRendered {
			msg = "resume + cover PDF rendered and stored"
		}
		return nil, &applicationPersistResult{
			JobID:       input.JobID,
			ResumePath:  res.ResumePath,
			CoverPath:   res.CoverPath,
			PDFRendered: res.PDFRendered,
			Message:     msg,
		}, nil
	})
}
