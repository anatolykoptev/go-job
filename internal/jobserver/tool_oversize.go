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

// toolErrorResult builds a *mcp.CallToolResult with IsError=true and the
// given message as the single text content item.
func toolErrorResult(msg string) *mcp.CallToolResult {
	r := &mcp.CallToolResult{}
	r.SetError(errors.New(msg))
	return r
}

// --- oversize_get ---

type oversizeGetInput struct {
	ID int64 `json:"id" jsonschema:"Oversize response ID returned in the envelope oversize_id field"`
}

func registerOversizeGet(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "oversize_get",
		Description: "Retrieve a previously spilled large MCP response by its oversize_id. Use this when a prior tool call returned an Envelope with oversize_id pointing to a stored payload.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input oversizeGetInput) (*mcp.CallToolResult, *oversize.Entry, error) {
		store := engine.GetOversizeStore()
		if store == nil {
			return toolErrorResult("oversize store not configured (DATABASE_URL not set)"), nil, nil
		}
		entry, err := store.Get(ctx, input.ID)
		if err != nil {
			if errors.Is(err, oversize.ErrNotFound) {
				return toolErrorResult(fmt.Sprintf("id %d not found", input.ID)), nil, nil
			}
			return nil, nil, fmt.Errorf("oversize_get: %w", err)
		}
		return nil, entry, nil
	})
}

// --- oversize_list ---

type oversizeListInput struct {
	Tool  string `json:"tool,omitempty"  jsonschema:"Filter by tool name (optional)"`
	Since string `json:"since,omitempty" jsonschema:"Filter entries created after this RFC3339 timestamp (optional)"`
	Limit int    `json:"limit,omitempty" jsonschema:"Max results (default 20, max 200)"`
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

func registerOversizeList(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "oversize_list",
		Description: "List recent spilled MCP responses. Filter by tool name and/or time. Returns metadata + sample without full payload.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input oversizeListInput) (*mcp.CallToolResult, oversizeListOutput, error) {
		store := engine.GetOversizeStore()
		if store == nil {
			return toolErrorResult("oversize store not configured (DATABASE_URL not set)"), oversizeListOutput{}, nil
		}

		f := oversize.ListFilter{
			ToolName: input.Tool,
			Limit:    input.Limit,
		}
		if input.Since != "" {
			t, err := time.Parse(time.RFC3339, input.Since)
			if err != nil {
				return nil, oversizeListOutput{}, fmt.Errorf("oversize_list: invalid since %q (want RFC3339): %w", input.Since, err)
			}
			f.Since = t
		}

		entries, err := store.List(ctx, f)
		if err != nil {
			return nil, oversizeListOutput{}, fmt.Errorf("oversize_list: %w", err)
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
	})
}

// --- oversize_purge ---

type oversizePurgeInput struct {
	OlderThanDays int `json:"older_than_days" jsonschema:"Delete entries older than this many days (min 1)"`
}

type oversizePurgeOutput struct {
	Deleted int64     `json:"deleted"`
	Before  time.Time `json:"before"`
}

func registerOversizePurge(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "oversize_purge",
		Description: "Delete spilled responses older than N days. Use to free space (typical retention: 7-30 days).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input oversizePurgeInput) (*mcp.CallToolResult, oversizePurgeOutput, error) {
		store := engine.GetOversizeStore()
		if store == nil {
			return toolErrorResult("oversize store not configured (DATABASE_URL not set)"), oversizePurgeOutput{}, nil
		}
		if input.OlderThanDays < 1 {
			return nil, oversizePurgeOutput{}, errors.New("older_than_days must be at least 1")
		}
		before := time.Now().Add(-time.Duration(input.OlderThanDays) * 24 * time.Hour)
		deleted, err := store.Purge(ctx, before)
		if err != nil {
			return nil, oversizePurgeOutput{}, fmt.Errorf("oversize_purge: %w", err)
		}
		return nil, oversizePurgeOutput{Deleted: deleted, Before: before}, nil
	})
}
