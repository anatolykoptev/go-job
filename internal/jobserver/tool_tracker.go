package jobserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type jobTrackerInput struct {
	Action  string `json:"action"             jsonschema:"Required. CRUD action: add, list, update"`
	Title   string `json:"title,omitempty"    jsonschema:"Job title (required for action=add)"`
	Company string `json:"company,omitempty"  jsonschema:"Company name (required for action=add)"`
	URL     string `json:"url,omitempty"      jsonschema:"Job URL (for action=add)"`
	Status  string `json:"status,omitempty"   jsonschema:"Status: saved, applied, interview, offer, rejected (for add or as filter for list)"`
	Notes   string `json:"notes,omitempty"    jsonschema:"Notes (for action=add or update)"`
	ID      int64  `json:"id,omitempty"       jsonschema:"Tracked job ID (required for action=update)"`
	Limit   int    `json:"limit,omitempty"    jsonschema:"Max results (for action=list)"`
}

func registerJobTracker(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "job_tracker",
		Description: "Track job applications. action=add saves a new job (title+company required); action=list returns tracked jobs (filter by status); action=update changes status/notes by ID.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input jobTrackerInput) (*mcp.CallToolResult, any, error) {
		switch input.Action {
		case "add":
			if input.Title == "" || input.Company == "" {
				return nil, nil, errors.New("title and company are required for action=add")
			}
			result, err := jobs.AddTrackedJob(ctx, jobs.JobTrackerAddInput{
				Title:   input.Title,
				Company: input.Company,
				URL:     input.URL,
				Status:  input.Status,
				Notes:   input.Notes,
			})
			if err != nil {
				return nil, nil, err
			}
			return nil, result, nil
		case "list":
			result, err := jobs.ListTrackedJobs(ctx, jobs.JobTrackerListInput{
				Status: input.Status,
				Limit:  input.Limit,
			})
			if err != nil {
				return nil, nil, err
			}
			return nil, result, nil
		case "update":
			if input.ID <= 0 {
				return nil, nil, errors.New("id is required for action=update")
			}
			result, err := jobs.UpdateTrackedJob(ctx, jobs.JobTrackerUpdateInput{
				ID:     input.ID,
				Status: input.Status,
				Notes:  input.Notes,
			})
			if err != nil {
				return nil, nil, err
			}
			return nil, result, nil
		default:
			return nil, nil, fmt.Errorf("unknown action %q: must be one of add, list, update", input.Action)
		}
	})
}
