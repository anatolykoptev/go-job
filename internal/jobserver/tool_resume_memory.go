package jobserver

import (
	"context"
	"errors"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerResumeMemorySearch(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "resume_memory_search",
		Description: "Semantically search the user's resume vectors in MemDB. Find relevant experiences, projects, skills, and agent-added notes by meaning, not just keywords. Use this to explore what the resume contains before generating content.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.ResumeMemorySearchInput) (*mcp.CallToolResult, *jobs.ResumeMemorySearchResult, error) {
		if input.Query == "" {
			return nil, nil, errors.New("query is required")
		}
		result, err := jobs.SearchResumeMemory(ctx, input.Query, input.TopK)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}

func registerResumeMemoryAdd(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "resume_memory_add",
		Description: "Add a note, career goal, preference, or other context to the user's resume memory in MemDB. These are stored as vectors and will be found by resume_memory_search. Use this to store insights discovered during conversation.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.ResumeMemoryAddInput) (*mcp.CallToolResult, *jobs.ResumeMemoryAddResult, error) {
		if input.Content == "" {
			return nil, nil, errors.New("content is required")
		}
		result, err := jobs.AddResumeMemory(ctx, input.Content, input.Type)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}

func registerResumeMemoryUpdate(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "resume_memory_update",
		Description: "Update an existing memory in MemDB by its ID (from resume_memory_search results). Replaces the old content while preserving the memory type. Use this to correct facts or update goals.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.ResumeMemoryUpdateInput) (*mcp.CallToolResult, *jobs.ResumeMemoryUpdateResult, error) {
		if input.MemoryID == "" {
			return nil, nil, errors.New("memory_id is required")
		}
		if input.Content == "" {
			return nil, nil, errors.New("content is required")
		}
		result, err := jobs.UpdateResumeMemory(ctx, input.MemoryID, input.Content)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}
