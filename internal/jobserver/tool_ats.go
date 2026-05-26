package jobserver

import (
	"context"
	"errors"

	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerATSURLParse(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "ats_url_parse",
		Description: "Parse a known ATS (Greenhouse, Ashby, Lever) job or board URL into structured platform/org/job_id/api_url fields. Returns platform=\"unknown\" if URL doesn't match a supported ATS.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(_ context.Context, _ *mcp.CallToolRequest, input struct {
		URL string `json:"url"`
	}) (*mcp.CallToolResult, *jobs.ATSURLInfo, error) {
		if input.URL == "" {
			return nil, nil, errors.New("url is required")
		}
		info, err := jobs.ParseATSURL(input.URL)
		if err != nil {
			return nil, nil, err
		}
		return nil, info, nil
	})
}

func registerATSBoardFetch(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "ats_board_fetch",
		Description: "Direct fetch of an ATS (Greenhouse, Ashby, Lever) job board by known org slug. Returns normalized job list with title, location, comp, URL. Optional query filter (case-insensitive title substring) and limit (default 100, max 500). Use ats_url_parse first if you have a URL but not the slug.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input jobs.FetchATSBoardInput) (*mcp.CallToolResult, *jobs.FetchATSBoardResult, error) {
		if input.Org == "" || input.Platform == "" {
			return nil, nil, errors.New("org and platform are required")
		}
		result, err := jobs.FetchATSBoard(ctx, input)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}
