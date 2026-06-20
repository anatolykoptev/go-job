package jobserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const huntListDescSnippetLen = 300

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

// huntListOutput is the output schema for the hunt_list tool.
// Entries must be []map[string]any (not any) so the go-sdk reflects it as a
// proper JSON Schema array-of-object rather than the boolean literal true that
// the MCP client rejects when it sees it under properties.<field>.
type huntListOutput struct {
	Kind    string           `json:"kind"`
	Entries []map[string]any `json:"entries"`
	Count   int              `json:"count"`
}

// toGenericSlice marshal/remarshals any typed slice into []map[string]any so
// the per-kind typed results ([]hunt.Job, []hunt.Bounty, …) can be assigned to
// huntListOutput.Entries without changing the JSON representation.
func toGenericSlice(v any) ([]map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("toGenericSlice marshal: %w", err)
	}
	var out []map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("toGenericSlice unmarshal: %w", err)
	}
	return out, nil
}

// truncateEntryDescriptions truncates the "description" field of each entry to
// huntListDescSnippetLen runes. Truncated entries have "description_truncated"
// set to true; short descriptions are left intact with no extra key added.
func truncateEntryDescriptions(entries []map[string]any) {
	for _, entry := range entries {
		raw, ok := entry["description"]
		if !ok {
			continue
		}
		desc, ok := raw.(string)
		if !ok {
			continue
		}
		runes := []rune(desc)
		if len(runes) > huntListDescSnippetLen {
			entry["description"] = string(runes[:huntListDescSnippetLen]) + "…"
			entry["description_truncated"] = true
		}
	}
}

func registerHuntList(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "hunt_list",
		Description: "List entries from the hunt DB. kind=jobs|bounties|freelance|security. For kind=bounties, open rows may trigger a background (non-blocking, off-request-path) GitHub status refresh; jobs/freelance/security are a plain DB read. description is a snippet (~300 chars); full text at the entry's url.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in huntListInput) (*mcp.CallToolResult, huntListOutput, error) {
		store := engine.GetHuntStore()
		if store == nil {
			return toolErrorResult("hunt store not configured (DATABASE_URL not set)"), huntListOutput{}, nil
		}
		limit := clampHuntLimit(in.Limit)

		var out huntListOutput
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
			generic, err := toGenericSlice(entries)
			if err != nil {
				return nil, huntListOutput{}, fmt.Errorf("hunt_list jobs serialize: %w", err)
			}
			truncateEntryDescriptions(generic)
			out = huntListOutput{Kind: huntKindJobs, Entries: generic, Count: len(entries)}
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
			generic, err := toGenericSlice(entries)
			if err != nil {
				return nil, huntListOutput{}, fmt.Errorf("hunt_list bounties serialize: %w", err)
			}
			truncateEntryDescriptions(generic)
			out = huntListOutput{Kind: huntKindBounties, Entries: generic, Count: len(entries)}
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
			generic, err := toGenericSlice(entries)
			if err != nil {
				return nil, huntListOutput{}, fmt.Errorf("hunt_list freelance serialize: %w", err)
			}
			truncateEntryDescriptions(generic)
			out = huntListOutput{Kind: huntKindFreelance, Entries: generic, Count: len(entries)}
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
			generic, err := toGenericSlice(entries)
			if err != nil {
				return nil, huntListOutput{}, fmt.Errorf("hunt_list security serialize: %w", err)
			}
			truncateEntryDescriptions(generic)
			out = huntListOutput{Kind: huntKindSecurity, Entries: generic, Count: len(entries)}
		default:
			return nil, huntListOutput{}, fmt.Errorf("unknown kind %q: must be one of jobs, bounties, freelance, security", in.Kind)
		}

		// Server-handled marker: bumped only when the request actually reached
		// the server and rows were fetched. A client-side transport drop (idle
		// keep-alive reuse race) never reaches here, so a missing increment for
		// a reported failure is positive evidence the close was client-side.
		engine.IncrHuntList(in.Kind)

		if cr, spilled := handleSpill(ctx, "hunt_list", out); spilled {
			return cr, huntListOutput{}, nil
		}
		return nil, out, nil
	})
}
