package jobserver

import (
	"context"
	"errors"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/anatolykoptev/go_job/internal/engine/jobs/applications"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerResumeScaffold registers the resume_scaffold MCP tool.
//
// Returns the approved header block (filled from the profile DB) plus the
// body shape contract as text. The caller authors the body; this gives it the
// mechanical parts it must not hand-type. ReadOnlyHint: true — it reads the
// profile DB and returns text, it writes nothing.
func registerResumeScaffold(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "resume_scaffold",
		Description: "Return the approved resume header block (filled from the profile DB) plus the body shape contract as text. The caller authors the body; this gives the mechanical parts (typst header geometry, contact line, shape rules) it must not hand-type. Use before authoring the body.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.ResumeScaffoldInput) (*mcp.CallToolResult, *jobs.ResumeScaffoldResult, error) {
		result, err := jobs.ScaffoldResume(ctx, input.Headline)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}

// registerResumeLint registers the resume_lint MCP tool.
//
// Validates caller-authored markdown against the shape contract and returns a
// structured verdict. ReadOnlyHint: true — it is a pure check, it writes nothing.
func registerResumeLint(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "resume_lint",
		Description: "Validate caller-authored resume markdown against the shape contract. Returns a structured verdict: ok plus findings[] with line, rule, detail. Catches the job-65473 defect (### entries with the descriptor collapsed in, no #### subtitle).",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.ResumeLintInput) (*mcp.CallToolResult, *jobs.LintVerdict, error) {
		if input.ResumeMD == "" {
			return nil, nil, errors.New("resume_md is required")
		}
		return nil, jobs.LintResume(input.ResumeMD), nil
	})
}

// registerResumeRender registers the resume_render MCP tool.
//
// Renders caller-authored markdown through the existing pdfrender.TypstAdapter
// into a drafts area (no job_id, nothing bound to a job record). Returns the
// lint findings for the same input so the caller sees them without a second
// call. Degrades to md-only when the renderer or its binaries are absent —
// matches application_persist's degradation. No ReadOnlyHint — it writes files.
func registerResumeRender(server *mcp.Server, authority *applications.Authority) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "resume_render",
		Description: "Render caller-authored resume markdown (header from resume_scaffold + body) to PDF via the existing typst adapter, writing to a drafts area (no job_id). " +
			"Returns resume_path, cover_path (when a cover was given), pages, words, pdf_rendered, and the resume_lint findings for the same input. " +
			"Degrades to md-only when the renderer or its binaries are absent. Use after resume_scaffold + caller-authored body.",
		// No ReadOnlyHint — this tool writes files to the drafts area.
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.ResumeRenderInput) (*mcp.CallToolResult, *jobs.ResumeRenderResult, error) {
		if input.Name == "" {
			return nil, nil, errors.New("name is required")
		}
		if input.ResumeMD == "" {
			return nil, nil, errors.New("resume_md is required")
		}
		// Reuse the Authority's renderer (one renderer, one binary-availability
		// probe at startup). The Authority is the composition root for the
		// renderer; resume_render is a sibling of application_persist, not a
		// replacement, and shares the same renderer instance.
		var renderer jobs.PDFRenderer
		if authority != nil {
			renderer = authority.Renderer()
		}
		result, err := jobs.RenderResume(ctx, renderer, input.Name, input.ResumeMD, input.CoverMD)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}
