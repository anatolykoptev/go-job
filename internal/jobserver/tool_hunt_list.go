package jobserver

// MCP tools for listing hunt entries (bounties, jobs, freelance, security programs).
//
// Each List call triggers the lazy background GitHub enricher via Store.ListBounties
// (the enricher fires on every ListBounties read for open rows that are overdue).
// This makes the enricher active even without a dedicated cron — any MCP caller
// that lists bounties implicitly keeps status data fresh.

import (
	"context"
	"fmt"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// clampHuntLimit applies default/max clamping for hunt list tool Limit inputs.
// Default is 50, max is 200 (matches hunt.Store.ListBounties ceiling of 500 but
// MCP tools cap lower to limit response size).
func clampHuntLimit(v int) int {
	if v <= 0 {
		return 50
	}
	if v > 200 {
		return 200
	}
	return v
}

// --- hunt_list_bounties ---

type huntListBountiesInput struct {
	Source        string   `json:"source,omitempty"         jsonschema:"Filter by source (e.g. algora, opire)"`
	Skills        []string `json:"skills,omitempty"         jsonschema:"Filter by required skills (any match)"`
	MinAmount     int64    `json:"min_amount,omitempty"     jsonschema:"Minimum bounty amount in USD cents"`
	IncludeClosed bool     `json:"include_closed,omitempty" jsonschema:"Include closed/merged bounties (default false = open only)"`
	Limit         int      `json:"limit,omitempty"          jsonschema:"Max results (default 50, max 200)"`
	Offset        int      `json:"offset,omitempty"         jsonschema:"Pagination offset"`
}

type huntListBountiesOutput struct {
	Entries []hunt.Bounty `json:"entries"`
	Count   int           `json:"count"`
}

func registerHuntListBounties(server *mcp.Server) { //nolint:dupl // intentional parallel structure across 4 typed list tools
	mcp.AddTool(server, &mcp.Tool{
		Name:        "hunt_list_bounties",
		Description: "List open-source bounties stored in the hunt DB. By default returns only open bounties. Each call triggers a lazy GitHub status enrichment pass for open rows.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in huntListBountiesInput) (*mcp.CallToolResult, huntListBountiesOutput, error) {
		store := engine.GetHuntStore()
		if store == nil {
			return toolErrorResult("hunt store not configured (DATABASE_URL not set)"), huntListBountiesOutput{}, nil
		}
		f := hunt.BountyFilter{
			Source:        in.Source,
			Skills:        in.Skills,
			MinAmount:     in.MinAmount,
			IncludeClosed: in.IncludeClosed,
			Limit:         clampHuntLimit(in.Limit),
			Offset:        in.Offset,
		}
		entries, err := store.ListBounties(ctx, f)
		if err != nil {
			return nil, huntListBountiesOutput{}, fmt.Errorf("hunt_list_bounties: %w", err)
		}
		if entries == nil {
			entries = []hunt.Bounty{}
		}
		return nil, huntListBountiesOutput{Entries: entries, Count: len(entries)}, nil
	})
}

// --- hunt_list_jobs ---

type huntListJobsInput struct {
	Source        string `json:"source,omitempty"         jsonschema:"Filter by source (e.g. linkedin, indeed)"`
	Company       string `json:"company,omitempty"        jsonschema:"Filter by company name (partial match)"`
	Remote        string `json:"remote,omitempty"         jsonschema:"Filter by remote type (e.g. 'remote', 'hybrid')"`
	IncludeClosed bool   `json:"include_closed,omitempty" jsonschema:"Include closed jobs (default false = open only)"`
	Limit         int    `json:"limit,omitempty"          jsonschema:"Max results (default 50, max 200)"`
	Offset        int    `json:"offset,omitempty"         jsonschema:"Pagination offset"`
}

type huntListJobsOutput struct {
	Entries []hunt.Job `json:"entries"`
	Count   int        `json:"count"`
}

func registerHuntListJobs(server *mcp.Server) { //nolint:dupl // intentional parallel structure across 4 typed list tools
	mcp.AddTool(server, &mcp.Tool{
		Name:        "hunt_list_jobs",
		Description: "List job listings stored in the hunt DB. By default returns only open jobs.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in huntListJobsInput) (*mcp.CallToolResult, huntListJobsOutput, error) {
		store := engine.GetHuntStore()
		if store == nil {
			return toolErrorResult("hunt store not configured (DATABASE_URL not set)"), huntListJobsOutput{}, nil
		}
		f := hunt.JobFilter{
			Source:        in.Source,
			Company:       in.Company,
			Remote:        in.Remote,
			IncludeClosed: in.IncludeClosed,
			Limit:         clampHuntLimit(in.Limit),
			Offset:        in.Offset,
		}
		entries, err := store.ListJobs(ctx, f)
		if err != nil {
			return nil, huntListJobsOutput{}, fmt.Errorf("hunt_list_jobs: %w", err)
		}
		if entries == nil {
			entries = []hunt.Job{}
		}
		return nil, huntListJobsOutput{Entries: entries, Count: len(entries)}, nil
	})
}

// --- hunt_list_freelance ---

type huntListFreelanceInput struct {
	Platform      string   `json:"platform,omitempty"       jsonschema:"Filter by platform (e.g. upwork, freelancer)"`
	Skills        []string `json:"skills,omitempty"         jsonschema:"Filter by required skills (any match)"`
	MinBudget     int      `json:"min_budget,omitempty"     jsonschema:"Minimum budget in USD"`
	IncludeClosed bool     `json:"include_closed,omitempty" jsonschema:"Include archived projects (default false = open only)"`
	Limit         int      `json:"limit,omitempty"          jsonschema:"Max results (default 50, max 200)"`
	Offset        int      `json:"offset,omitempty"         jsonschema:"Pagination offset"`
}

type huntListFreelanceOutput struct {
	Entries []hunt.Freelance `json:"entries"`
	Count   int              `json:"count"`
}

func registerHuntListFreelance(server *mcp.Server) { //nolint:dupl // intentional parallel structure across 4 typed list tools
	mcp.AddTool(server, &mcp.Tool{
		Name:        "hunt_list_freelance",
		Description: "List freelance projects stored in the hunt DB. By default returns only open/active projects.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in huntListFreelanceInput) (*mcp.CallToolResult, huntListFreelanceOutput, error) {
		store := engine.GetHuntStore()
		if store == nil {
			return toolErrorResult("hunt store not configured (DATABASE_URL not set)"), huntListFreelanceOutput{}, nil
		}
		f := hunt.FreelanceFilter{
			Platform:      in.Platform,
			Skills:        in.Skills,
			MinBudget:     in.MinBudget,
			IncludeClosed: in.IncludeClosed,
			Limit:         clampHuntLimit(in.Limit),
			Offset:        in.Offset,
		}
		entries, err := store.ListFreelance(ctx, f)
		if err != nil {
			return nil, huntListFreelanceOutput{}, fmt.Errorf("hunt_list_freelance: %w", err)
		}
		if entries == nil {
			entries = []hunt.Freelance{}
		}
		return nil, huntListFreelanceOutput{Entries: entries, Count: len(entries)}, nil
	})
}

// --- hunt_list_security ---

type huntListSecurityInput struct {
	Platform      string `json:"platform,omitempty"       jsonschema:"Filter by platform (e.g. hackerone, bugcrowd)"`
	MinBounty     int    `json:"min_bounty,omitempty"     jsonschema:"Minimum max bounty in USD"`
	IncludeClosed bool   `json:"include_closed,omitempty" jsonschema:"Include archived programs (default false = open only)"`
	Limit         int    `json:"limit,omitempty"          jsonschema:"Max results (default 50, max 200)"`
	Offset        int    `json:"offset,omitempty"         jsonschema:"Pagination offset"`
}

type huntListSecurityOutput struct {
	Entries []hunt.Security `json:"entries"`
	Count   int             `json:"count"`
}

func registerHuntListSecurity(server *mcp.Server) { //nolint:dupl // intentional parallel structure across 4 typed list tools
	mcp.AddTool(server, &mcp.Tool{
		Name:        "hunt_list_security",
		Description: "List security bug bounty programs stored in the hunt DB. By default returns only open/active programs.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in huntListSecurityInput) (*mcp.CallToolResult, huntListSecurityOutput, error) {
		store := engine.GetHuntStore()
		if store == nil {
			return toolErrorResult("hunt store not configured (DATABASE_URL not set)"), huntListSecurityOutput{}, nil
		}
		f := hunt.SecurityFilter{
			Platform:      in.Platform,
			MinBounty:     in.MinBounty,
			IncludeClosed: in.IncludeClosed,
			Limit:         clampHuntLimit(in.Limit),
			Offset:        in.Offset,
		}
		entries, err := store.ListSecurity(ctx, f)
		if err != nil {
			return nil, huntListSecurityOutput{}, fmt.Errorf("hunt_list_security: %w", err)
		}
		if entries == nil {
			entries = []hunt.Security{}
		}
		return nil, huntListSecurityOutput{Entries: entries, Count: len(entries)}, nil
	})
}
