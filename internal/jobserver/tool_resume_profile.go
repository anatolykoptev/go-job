package jobserver

import (
	"context"
	"errors"

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

// registerResumeProfileSync exposes the full re-sync of the structured-profile
// derived vectors through the existing MCP tool surface (no new binary). It
// re-derives experiences/projects/achievements vectors from the current entity
// state, removes orphans, and degrades to NULL embeddings on embedder outage —
// run it to backfill after an embedder was down, or to reconcile vectors with
// profile edits made out-of-band.
func registerResumeProfileSync(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "resume_profile_sync",
		Description: "Re-sync the structured resume profile into the resume_vectors store: re-derive experience/project/achievement vectors from the current DB entities, update changed rows, remove orphaned rows (entity deleted), and backfill NULL embeddings. Use after profile edits or an embedder outage. Manual resume_memory notes are never touched.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ engine.ResumeProfileSyncInput) (*mcp.CallToolResult, *jobs.ResumeProfileSyncResult, error) {
		db := jobs.GetResumeDB()
		if db == nil {
			return nil, nil, errors.New("resume DB not configured (set DATABASE_URL)")
		}
		personID := db.GetLatestPersonID(ctx)
		if personID == 0 {
			return nil, nil, errors.New("no resume found — use master_resume_build first")
		}
		result, err := jobs.SyncProfileVectorsReported(ctx, personID)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}
