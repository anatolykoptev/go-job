package jobserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type researchInput struct {
	Subject    string `json:"subject"              jsonschema:"Required. What to research: salary, company, person"`
	Role       string `json:"role,omitempty"       jsonschema:"Role/job title (required for subject=salary)"`
	Location   string `json:"location,omitempty"   jsonschema:"Location filter (for subject=salary/person)"`
	Experience string `json:"experience,omitempty" jsonschema:"Experience level (for subject=salary)"`
	Company    string `json:"company,omitempty"    jsonschema:"Company name (required for subject=company or helpful for subject=person)"`
	Name       string `json:"name,omitempty"       jsonschema:"Person name (required for subject=person)"`
	JobTitle   string `json:"job_title,omitempty"  jsonschema:"Person's job title (for subject=person)"`
}

func registerResearch(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "research",
		Description: "Research salary, company, or person. subject=salary (role required) returns p25/median/p75 salary data; subject=company (company required) returns funding/tech/culture; subject=person (name required) returns background/interests/interview tips.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input researchInput) (*mcp.CallToolResult, any, error) {
		switch input.Subject {
		case "salary":
			if input.Role == "" {
				return nil, nil, errors.New("role is required for subject=salary")
			}
			result, err := jobs.ResearchSalary(ctx, input.Role, input.Location, input.Experience)
			if err != nil {
				return nil, nil, err
			}
			return nil, result, nil
		case "company":
			if input.Company == "" {
				return nil, nil, errors.New("company is required for subject=company")
			}
			result, err := jobs.ResearchCompany(ctx, input.Company)
			if err != nil {
				return nil, nil, err
			}
			return nil, result, nil
		case "person":
			if input.Name == "" {
				return nil, nil, errors.New("name is required for subject=person")
			}
			result, err := jobs.ResearchPerson(ctx, input.Name, input.Company, input.JobTitle)
			if err != nil {
				return nil, nil, err
			}
			return nil, result, nil
		default:
			return nil, nil, fmt.Errorf("unknown subject %q: must be one of salary, company, person", input.Subject)
		}
	})
}
