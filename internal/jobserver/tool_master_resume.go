package jobserver

import (
	"context"
	"errors"

	"github.com/anatolykoptev/go_job/internal/accounts"
	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerMasterResumeBuild(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "master_resume_build",
		Description: "Build a master resume from your full resume text. Parses into a structured knowledge graph (skills, experiences, projects, achievements) with vector embeddings for semantic search. Pass merge=true to fold the document into the existing profile instead (preserves entities the document omits and manual edits — same consent + atomicity). DESTRUCTIVE when merge is omitted: rebuilding when a profile already exists replaces the existing profile (skills, projects, experiences, achievements, educations, certifications, domains, methodologies rebuilt from the text; upwork profile and headline/hourly_rate carry over) and rebuilds from scratch. When a profile already exists the call REFUSES unless replace_person_id names the id of the profile actually present (the refuse error names it) — a blind retry carrying a stale id fails once the profile id has changed. A failed, timed-out, or cancelled rebuild leaves the existing profile intact (the clear and all inserts share one transaction). Run once for a fresh profile, then use resume_generate to create tailored versions.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.MasterResumeBuildInput) (*mcp.CallToolResult, *jobs.MasterResumeBuildResult, error) {
		if input.Resume == "" {
			return nil, nil, errors.New("resume is required")
		}
		aid, ok := accounts.AccountFrom(ctx)
		if !ok {
			return nil, nil, errNoAccountIdentity
		}
		var result *jobs.MasterResumeBuildResult
		var err error
		if input.Merge {
			result, err = jobs.BuildMergedResume(ctx, aid, input.Resume, input.ReplacePersonID)
		} else {
			result, err = jobs.BuildMasterResume(ctx, aid, input.Resume, input.ReplacePersonID)
		}
		if err != nil {
			return nil, nil, err
		}
		if cr, spilled := handleSpill(ctx, "master_resume_build", result); spilled {
			return cr, nil, nil
		}
		return nil, result, nil
	})
}
