package jobserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/oversize"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolErrorResult builds a *mcp.CallToolResult with IsError=true.
func toolErrorResult(msg string) *mcp.CallToolResult {
	r := &mcp.CallToolResult{}
	r.SetError(errors.New(msg))
	return r
}

// EntrySummary is an oversize.Entry with Payload stripped — only metadata + Sample.
type EntrySummary struct {
	ID        int64           `json:"id"`
	ToolName  string          `json:"tool_name"`
	QueryHash string          `json:"query_hash,omitempty"`
	SizeBytes int             `json:"size_bytes"`
	SHA256    string          `json:"sha256"`
	Sample    json.RawMessage `json:"sample,omitempty"`
	ItemCount int             `json:"item_count"`
	CreatedAt time.Time       `json:"created_at"`
}

type oversizeListOutput struct {
	Entries []EntrySummary `json:"entries"`
	Count   int            `json:"count"`
}

type oversizeInput struct {
	Op            string `json:"op"                        jsonschema:"Required. Operation: get, list, purge"`
	ID            int64  `json:"id,omitempty"              jsonschema:"Oversize response ID (required for op=get)"`
	Tool          string `json:"tool,omitempty"            jsonschema:"Filter by tool name (for op=list)"`
	Since         string `json:"since,omitempty"           jsonschema:"RFC3339 timestamp filter (for op=list)"`
	Limit         int    `json:"limit,omitempty"           jsonschema:"Max results (for op=list, default 20)"`
	OlderThanDays int    `json:"older_than_days,omitempty" jsonschema:"Delete entries older than N days (required for op=purge, min 1)"`
}

func registerOversize(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "oversize",
		Description: "Manage spilled large MCP responses. op=get retrieves by id; op=list shows recent entries (filter by tool/since); op=purge deletes entries older than N days.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input oversizeInput) (*mcp.CallToolResult, any, error) {
		store := engine.GetOversizeStore()
		if store == nil {
			return toolErrorResult("oversize store not configured (DATABASE_URL not set)"), nil, nil
		}
		switch input.Op {
		case "get":
			if input.ID <= 0 {
				return nil, nil, errors.New("id is required for op=get")
			}
			entry, err := store.Get(ctx, input.ID)
			if err != nil {
				if errors.Is(err, oversize.ErrNotFound) {
					return toolErrorResult(fmt.Sprintf("id %d not found", input.ID)), nil, nil
				}
				return nil, nil, fmt.Errorf("oversize get: %w", err)
			}
			return nil, entry, nil
		case "list":
			f := oversize.ListFilter{
				ToolName: input.Tool,
				Limit:    input.Limit,
			}
			if input.Since != "" {
				t, err := time.Parse(time.RFC3339, input.Since)
				if err != nil {
					return nil, nil, fmt.Errorf("oversize list: invalid since %q (want RFC3339): %w", input.Since, err)
				}
				f.Since = t
			}
			entries, err := store.List(ctx, f)
			if err != nil {
				return nil, nil, fmt.Errorf("oversize list: %w", err)
			}
			summaries := make([]EntrySummary, 0, len(entries))
			for _, e := range entries {
				summaries = append(summaries, EntrySummary{
					ID:        e.ID,
					ToolName:  e.ToolName,
					QueryHash: e.QueryHash,
					SizeBytes: e.SizeBytes,
					SHA256:    e.SHA256,
					Sample:    e.Sample,
					ItemCount: e.ItemCount,
					CreatedAt: e.CreatedAt,
				})
			}
			return nil, oversizeListOutput{Entries: summaries, Count: len(summaries)}, nil
		case "purge":
			if input.OlderThanDays < 1 {
				return nil, nil, errors.New("older_than_days must be at least 1")
			}
			before := time.Now().Add(-time.Duration(input.OlderThanDays) * 24 * time.Hour)
			deleted, err := store.Purge(ctx, before)
			if err != nil {
				return nil, nil, fmt.Errorf("oversize purge: %w", err)
			}
			return nil, map[string]any{"deleted": deleted, "before": before}, nil
		default:
			return nil, nil, fmt.Errorf("unknown op %q: must be one of get, list, purge", input.Op)
		}
	})
}
