package jobserver

import (
	"context"
	"fmt"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerSalaryResearch(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "salary_research",
		Description: "Research salary ranges for a role and location. Returns p25/median/p75 percentiles with sources (levels.fyi, Glassdoor, LinkedIn, hh.ru, Хабр). For Russian locations returns RUB, otherwise USD.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.SalaryResearchInput) (*mcp.CallToolResult, *jobs.SalaryResearchResult, error) {
		if input.Role == "" {
			return nil, nil, fmt.Errorf("role is required")
		}
		result, err := jobs.ResearchSalary(ctx, input.Role, input.Location, input.Experience)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}

func registerCompanyResearch(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "company_research",
		Description: "Research a company for interview preparation or job evaluation. Returns size, funding, tech stack, culture notes, recent news, Glassdoor rating, and an overall summary for job seekers.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.CompanyResearchInput) (*mcp.CallToolResult, *jobs.CompanyResearchResult, error) {
		if input.Company == "" {
			return nil, nil, fmt.Errorf("company is required")
		}
		result, err := jobs.ResearchCompany(ctx, input.Company)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}

func registerPersonResearch(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "person_research",
		Description: "Research a person (hiring manager, interviewer, recruiter) from open sources: LinkedIn, GitHub, web, Habr, and Twitter/X via go-hully. Returns background, skills, interests, recent activity, common ground, and specific interview tips. Use before interviews to build rapport and prepare relevant talking points.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.PersonResearchInput) (*mcp.CallToolResult, *jobs.PersonProfile, error) {
		if input.Name == "" {
			return nil, nil, fmt.Errorf("name is required")
		}
		result, err := jobs.ResearchPerson(ctx, input.Name, input.Company, input.JobTitle)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	})
}
