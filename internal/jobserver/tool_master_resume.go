package jobserver

import (
	"context"
	"errors"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerMasterResumeBuild(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "master_resume_build",
		Description: "Build a master resume from your full resume text. Parses into a structured knowledge graph (skills, experiences, projects, achievements) with vector embeddings for semantic search. Run once, then use resume_generate to create tailored versions.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.MasterResumeBuildInput) (*mcp.CallToolResult, *jobs.MasterResumeBuildResult, error) {
		if input.Resume == "" {
			return nil, nil, errors.New("resume is required")
		}
		result, err := jobs.BuildMasterResume(ctx, input.Resume)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}
