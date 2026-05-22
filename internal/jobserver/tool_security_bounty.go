package jobserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type securitySearchInput struct {
	Platform string `json:"platform" jsonschema:"Filter by platform: hackerone, bugcrowd, intigriti, yeswehack, immunefi. Empty returns all."`
	Query    string `json:"query" jsonschema:"Search keyword to filter programs by name or scope (e.g. 'crypto', 'api'). Empty returns all."`
}

func registerSecurityBountySearch(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "security_bounty_search",
		Description: "Search for security bug bounty programs across HackerOne, Bugcrowd, Intigriti, YesWeHack, and Immunefi. Returns program name, platform, max bounty, and in-scope targets.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input securitySearchInput) (*mcp.CallToolResult, engine.SmartSearchOutput, error) {
		btdPrograms, btdErr := jobs.SearchSecurityPrograms(ctx, 500)
		immPrograms, immErr := jobs.SearchImmunefi(ctx, 500)

		btdPrograms = append(btdPrograms, immPrograms...)
		all := btdPrograms
		if len(all) == 0 {
			if btdErr != nil || immErr != nil {
				return nil, engine.SmartSearchOutput{}, errors.New("all security sources failed")
			}
			return nil, engine.SmartSearchOutput{Query: input.Query, Answer: "No programs found."}, nil
		}

		filtered := filterSecurityPrograms(all, input)
		persistSecurityPrograms(ctx, all) // persist unfiltered; filter is a view concern
		jsonBytes, _ := json.Marshal(filtered)
		out := engine.SmartSearchOutput{
			Query:   input.Query,
			Answer:  string(jsonBytes),
			Sources: []engine.SourceItem{},
		}
		if cr, spilled := handleSpill(ctx, "security_bounty_search", out); spilled {
			var zero engine.SmartSearchOutput
			return cr, zero, nil
		}
		return nil, out, nil
	})
}

// persistSecurityPrograms writes security programs into the hunt store (best-effort).
func persistSecurityPrograms(ctx context.Context, programs []engine.SecurityProgram) {
	store := engine.GetHuntStore()
	if store == nil {
		return
	}
	for _, sp := range programs {
		if sp.URL == "" {
			continue
		}
		_, outcome, err := store.UpsertSecurity(ctx, jobs.SecurityProgramToHunt(sp))
		engine.IncrHuntIngest(hunt.KindSecurity, outcome.String())
		if err != nil {
			slog.Warn("hunt: upsert security failed", slog.Any("error", err))
		}
	}
}

func filterSecurityPrograms(programs []engine.SecurityProgram, input securitySearchInput) []engine.SecurityProgram {
	if input.Platform == "" && input.Query == "" {
		if len(programs) > 100 {
			return programs[:100]
		}
		return programs
	}

	var filtered []engine.SecurityProgram
	for _, p := range programs {
		if input.Platform != "" && p.Platform != input.Platform {
			continue
		}
		if input.Query != "" {
			q := strings.ToLower(input.Query)
			if !strings.Contains(strings.ToLower(p.Name), q) && !targetsContain(p.Targets, q) {
				continue
			}
		}
		filtered = append(filtered, p)
	}
	if len(filtered) > 100 {
		filtered = filtered[:100]
	}
	return filtered
}

func targetsContain(targets []string, query string) bool {
	for _, t := range targets {
		if strings.Contains(strings.ToLower(t), query) {
			return true
		}
	}
	return false
}
