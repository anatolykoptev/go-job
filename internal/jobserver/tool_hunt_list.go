package jobserver

import (
	"context"
	"fmt"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func clampHuntLimit(v int) int {
	if v <= 0 {
		return 50
	}
	if v > 200 {
		return 200
	}
	return v
}

type huntListInput struct {
	Kind          string   `json:"kind"                     jsonschema:"Required. Entry type: jobs, bounties, freelance, security"`
	Source        string   `json:"source,omitempty"         jsonschema:"Filter by source (applies to all kinds)"`
	Company       string   `json:"company,omitempty"        jsonschema:"Filter by company (applies when kind=jobs)"`
	Remote        string   `json:"remote,omitempty"         jsonschema:"Filter by remote type (applies when kind=jobs)"`
	Skills        []string `json:"skills,omitempty"         jsonschema:"Filter by skills (applies when kind=bounties or freelance)"`
	MinAmount     int64    `json:"min_amount,omitempty"     jsonschema:"Minimum bounty amount in USD cents (applies when kind=bounties)"`
	Platform      string   `json:"platform,omitempty"       jsonschema:"Filter by platform (applies when kind=freelance or security)"`
	MinBudget     int      `json:"min_budget,omitempty"     jsonschema:"Minimum budget in USD (applies when kind=freelance)"`
	MinBounty     int      `json:"min_bounty,omitempty"     jsonschema:"Minimum max bounty in USD (applies when kind=security)"`
	IncludeClosed bool     `json:"include_closed,omitempty" jsonschema:"Include closed/archived entries"`
	Limit         int      `json:"limit,omitempty"          jsonschema:"Max results (default 50, max 200)"`
	Offset        int      `json:"offset,omitempty"         jsonschema:"Pagination offset"`
}

type huntListOutput struct {
	Kind    string `json:"kind"`
	Entries any    `json:"entries"`
	Count   int    `json:"count"`
}

func registerHuntList(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "hunt_list",
		Description: "List entries from the hunt DB. kind=jobs|bounties|freelance|security. Each call triggers lazy enrichment for open rows.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in huntListInput) (*mcp.CallToolResult, huntListOutput, error) {
		store := engine.GetHuntStore()
		if store == nil {
			return toolErrorResult("hunt store not configured (DATABASE_URL not set)"), huntListOutput{}, nil
		}
		limit := clampHuntLimit(in.Limit)
		switch in.Kind {
		case huntKindJobs:
			f := hunt.JobFilter{
				Source:        in.Source,
				Company:       in.Company,
				Remote:        in.Remote,
				IncludeClosed: in.IncludeClosed,
				Limit:         limit,
				Offset:        in.Offset,
			}
			entries, err := store.ListJobs(ctx, f)
			if err != nil {
				return nil, huntListOutput{}, fmt.Errorf("hunt_list jobs: %w", err)
			}
			if entries == nil {
				entries = []hunt.Job{}
			}
			return nil, huntListOutput{Kind: huntKindJobs, Entries: entries, Count: len(entries)}, nil
		case huntKindBounties:
			f := hunt.BountyFilter{
				Source:        in.Source,
				Skills:        in.Skills,
				MinAmount:     in.MinAmount,
				IncludeClosed: in.IncludeClosed,
				Limit:         limit,
				Offset:        in.Offset,
			}
			entries, err := store.ListBounties(ctx, f)
			if err != nil {
				return nil, huntListOutput{}, fmt.Errorf("hunt_list bounties: %w", err)
			}
			if entries == nil {
				entries = []hunt.Bounty{}
			}
			return nil, huntListOutput{Kind: huntKindBounties, Entries: entries, Count: len(entries)}, nil
		case huntKindFreelance:
			f := hunt.FreelanceFilter{
				Platform:      in.Platform,
				Skills:        in.Skills,
				MinBudget:     in.MinBudget,
				IncludeClosed: in.IncludeClosed,
				Limit:         limit,
				Offset:        in.Offset,
			}
			entries, err := store.ListFreelance(ctx, f)
			if err != nil {
				return nil, huntListOutput{}, fmt.Errorf("hunt_list freelance: %w", err)
			}
			if entries == nil {
				entries = []hunt.Freelance{}
			}
			return nil, huntListOutput{Kind: huntKindFreelance, Entries: entries, Count: len(entries)}, nil
		case huntKindSecurity:
			f := hunt.SecurityFilter{
				Platform:      in.Platform,
				MinBounty:     in.MinBounty,
				IncludeClosed: in.IncludeClosed,
				Limit:         limit,
				Offset:        in.Offset,
			}
			entries, err := store.ListSecurity(ctx, f)
			if err != nil {
				return nil, huntListOutput{}, fmt.Errorf("hunt_list security: %w", err)
			}
			if entries == nil {
				entries = []hunt.Security{}
			}
			return nil, huntListOutput{Kind: huntKindSecurity, Entries: entries, Count: len(entries)}, nil
		default:
			return nil, huntListOutput{}, fmt.Errorf("unknown kind %q: must be one of jobs, bounties, freelance, security", in.Kind)
		}
	})
}
