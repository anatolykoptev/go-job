package jobserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type atsInput struct {
	Op       string `json:"op"                 jsonschema:"Required. Operation: parse, fetch"`
	URL      string `json:"url,omitempty"      jsonschema:"ATS board or job URL to parse (required for op=parse)"`
	Org      string `json:"org,omitempty"      jsonschema:"ATS org slug (required for op=fetch)"`
	Platform string `json:"platform,omitempty" jsonschema:"ATS platform: greenhouse, ashby, lever (required for op=fetch)"`
	Query    string `json:"query,omitempty"    jsonschema:"Title filter substring (for op=fetch)"`
	Limit    int    `json:"limit,omitempty"    jsonschema:"Max results (for op=fetch, default 100)"`
}

func registerATS(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "ats",
		Description: "ATS (Greenhouse, Ashby, Lever) tools. op=parse — parse a URL into platform/org/job_id fields; op=fetch — fetch all jobs from a known org by slug.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input atsInput) (*mcp.CallToolResult, any, error) {
		switch input.Op {
		case "parse":
			if input.URL == "" {
				return nil, nil, errors.New("url is required for op=parse")
			}
			info, err := jobs.ParseATSURL(input.URL)
			if err != nil {
				return nil, nil, err
			}
			return nil, info, nil
		case "fetch":
			if input.Org == "" || input.Platform == "" {
				return nil, nil, errors.New("org and platform are required for op=fetch")
			}
			result, err := jobs.FetchATSBoard(ctx, jobs.FetchATSBoardInput{
				Org:      input.Org,
				Platform: input.Platform,
				Query:    input.Query,
				Limit:    input.Limit,
			})
			if err != nil {
				return nil, nil, err
			}
			return nil, result, nil
		default:
			return nil, nil, fmt.Errorf("unknown op %q: must be one of parse, fetch", input.Op)
		}
	})
}
