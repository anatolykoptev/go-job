package jobserver

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerOpportunityAnalyze(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "opportunity_analyze",
		Description: "Analyze any income opportunity by URL. Auto-detects type: GitHub issue URLs are analyzed as code bounties (complexity, $/hr, competing PRs), security platform URLs show program details, freelance URLs show job details.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.OpportunityAnalyzeInput) (*mcp.CallToolResult, engine.SmartSearchOutput, error) {
		if input.URL == "" {
			return nil, engine.SmartSearchOutput{}, errors.New("url is required")
		}

		typ := jobs.DetectOpportunityType(input.URL)
		if typ == "" {
			return nil, engine.SmartSearchOutput{}, errors.New("cannot detect opportunity type from URL; supported: GitHub issues, HackerOne, Bugcrowd, Intigriti, YesWeHack, Immunefi, RemoteOK, Himalayas, Upwork, Freelancer")
		}

		var analysis engine.OpportunityAnalysis

		switch typ {
		case "bounty":
			ba, err := jobs.AnalyzeBounty(ctx, input.URL)
			if err != nil {
				return nil, engine.SmartSearchOutput{}, err
			}
			analysis = engine.OpportunityAnalysis{
				Type:    "bounty",
				Title:   ba.Title,
				URL:     input.URL,
				Reward:  ba.Amount,
				Verdict: ba.Verdict,
				Summary: ba.Summary,
				Details: ba,
			}

		case "security":
			analysis = engine.OpportunityAnalysis{
				Type:    "security",
				Title:   "Security Program",
				URL:     input.URL,
				Summary: "Security programs require manual analysis. Visit the program page for scope, rules, and reward details. Use security_recon tools (ox-browser security_scan, go-code code_health) to scan targets.",
				Verdict: "manual",
			}

		case "freelance":
			analysis = engine.OpportunityAnalysis{
				Type:    "freelance",
				Title:   "Freelance Opportunity",
				URL:     input.URL,
				Summary: "Freelance opportunities require manual review. Visit the listing page for details. Use application_prep to prepare your application.",
				Verdict: "manual",
			}
		}

		jsonBytes, err := json.Marshal(analysis)
		if err != nil {
			return nil, engine.SmartSearchOutput{}, errors.New("json marshal failed")
		}

		return nil, engine.SmartSearchOutput{
			Query:   input.URL,
			Answer:  string(jsonBytes),
			Sources: []engine.SourceItem{},
		}, nil
	})
}
