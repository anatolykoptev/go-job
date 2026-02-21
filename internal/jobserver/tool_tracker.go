package jobserver

import (
	"context"
	"errors"

	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerJobTrackerAdd(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "job_tracker_add",
		Description: "Save a job to the local tracker (SQLite). Status options: saved (default), applied, interview, offer, rejected. Returns the assigned ID for future updates.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input jobs.JobTrackerAddInput) (*mcp.CallToolResult, *jobs.JobTrackerResult, error) {
		if input.Title == "" || input.Company == "" {
			return nil, nil, errors.New("title and company are required")
		}
		result, err := jobs.AddTrackedJob(ctx, input)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}

func registerJobTrackerList(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "job_tracker_list",
		Description: "List tracked job applications. Optionally filter by status: saved, applied, interview, offer, rejected. Returns jobs sorted by most recently updated.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input jobs.JobTrackerListInput) (*mcp.CallToolResult, *jobs.JobTrackerListResult, error) {
		result, err := jobs.ListTrackedJobs(ctx, input)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}

func registerJobTrackerUpdate(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "job_tracker_update",
		Description: "Update status or notes for a tracked job by ID. Status options: saved, applied, interview, offer, rejected. Get IDs from job_tracker_list.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input jobs.JobTrackerUpdateInput) (*mcp.CallToolResult, *jobs.JobTrackerResult, error) {
		if input.ID <= 0 {
			return nil, nil, errors.New("id is required")
		}
		result, err := jobs.UpdateTrackedJob(ctx, input)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}
