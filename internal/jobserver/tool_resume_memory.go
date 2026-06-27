package jobserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type resumeMemoryInput struct {
	Op       string `json:"op"                  jsonschema:"Required. Operation: search, add, update"`
	Query    string `json:"query,omitempty"     jsonschema:"Search query (required for op=search)"`
	TopK     int    `json:"top_k,omitempty"     jsonschema:"Max results (for op=search, default 5)"`
	Content  string `json:"content,omitempty"   jsonschema:"Memory content (required for op=add/update)"`
	Type     string `json:"type,omitempty"      jsonschema:"Memory type: experience, skill, goal, preference, other (for op=add)"`
	MemoryID string `json:"memory_id,omitempty" jsonschema:"Memory ID from search results (required for op=update)"`
}

func registerResumeMemory(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "resume_memory",
		Description: "Manage resume memory in postgres (pgvector + FTS). op=search finds relevant experiences/projects/skills by query; op=add stores a new note/goal/preference; op=update replaces an existing memory by memory_id.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input resumeMemoryInput) (*mcp.CallToolResult, any, error) {
		switch input.Op {
		case "search":
			if input.Query == "" {
				return nil, nil, errors.New("query is required for op=search")
			}
			result, err := jobs.SearchResumeMemory(ctx, input.Query, input.TopK)
			if err != nil {
				return nil, nil, err
			}
			return nil, result, nil
		case "add":
			if input.Content == "" {
				return nil, nil, errors.New("content is required for op=add")
			}
			result, err := jobs.AddResumeMemory(ctx, input.Content, input.Type)
			if err != nil {
				return nil, nil, err
			}
			return nil, result, nil
		case "update":
			if input.MemoryID == "" {
				return nil, nil, errors.New("memory_id is required for op=update")
			}
			if input.Content == "" {
				return nil, nil, errors.New("content is required for op=update")
			}
			result, err := jobs.UpdateResumeMemory(ctx, input.MemoryID, input.Content)
			if err != nil {
				return nil, nil, err
			}
			return nil, result, nil
		default:
			return nil, nil, fmt.Errorf("unknown op %q: must be one of search, add, update", input.Op)
		}
	})
}
