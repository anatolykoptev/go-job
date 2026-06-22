package jobserver

import (
	"context"
	"errors"
	"fmt"

	linkedin "github.com/anatolykoptev/go-linkedin"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type linkedInInput struct {
	Op       string `json:"op"                 jsonschema:"Required. Operation: profile, company, jobs, search, posts, rating"`
	Handle   string `json:"handle,omitempty"   jsonschema:"LinkedIn handle (required for op=profile/posts/rating)"`
	Company  string `json:"company,omitempty"  jsonschema:"Company slug (required for op=company)"`
	Query    string `json:"query,omitempty"    jsonschema:"Search keywords (required for op=jobs/search)"`
	Type     string `json:"type,omitempty"     jsonschema:"Search type: people, companies (for op=search, default: people)"`
	Location string `json:"location,omitempty" jsonschema:"Location filter (for op=jobs)"`
	Remote   string `json:"remote,omitempty"   jsonschema:"Work type: remote, hybrid, onsite (for op=jobs)"`
	Limit    int    `json:"limit,omitempty"    jsonschema:"Max results"`
}

func registerLinkedIn(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        platLinkedIn,
		Description: "LinkedIn data via Voyager API. op=profile (handle required) — full profile; op=company (company slug required) — company page; op=jobs (query required) — job listings; op=search (query required) — people/companies search; op=posts (handle required) — profile posts; op=rating (handle required) — profile influence score.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input linkedInInput) (*mcp.CallToolResult, any, error) {
		switch input.Op {
		case linkedInOpProfile:
			if input.Handle == "" {
				return nil, nil, errors.New("handle is required for op=profile")
			}
			profile, err := jobs.VoyagerProfile(ctx, input.Handle)
			if err != nil {
				return nil, nil, err
			}
			if cr, spilled := handleSpill(ctx, platLinkedIn, profile); spilled {
				return cr, nil, nil
			}
			return nil, profile, nil
		case linkedInOpCompany:
			if input.Company == "" {
				return nil, nil, errors.New("company is required for op=company")
			}
			company, err := jobs.VoyagerCompany(ctx, input.Company)
			if err != nil {
				return nil, nil, err
			}
			return nil, company, nil
		case linkedInOpJobs:
			if input.Query == "" {
				return nil, nil, errors.New("query is required for op=jobs")
			}
			limit := input.Limit
			if limit <= 0 {
				limit = 10
			}
			if limit > 25 {
				limit = 25
			}
			result, err := jobs.VoyagerJobs(ctx, linkedin.JobSearchParams{
				Query:    input.Query,
				Location: input.Location,
				Remote:   input.Remote,
				Limit:    limit,
			})
			if err != nil {
				return nil, nil, err
			}
			out := map[string]any{"query": input.Query, "count": len(result), "jobs": result}
			if cr, spilled := handleSpill(ctx, platLinkedIn, out); spilled {
				return cr, nil, nil
			}
			return nil, out, nil
		case linkedInOpSearch:
			if input.Query == "" {
				return nil, nil, errors.New("query is required for op=search")
			}
			limit := input.Limit
			if limit <= 0 {
				limit = 10
			}
			searchType := "people"
			if input.Type == "companies" {
				searchType = "companies"
			}
			var results []linkedin.SearchResult
			var err error
			if searchType == "companies" {
				results, err = jobs.VoyagerSearchCompanies(ctx, input.Query, limit)
			} else {
				results, err = jobs.VoyagerSearchPeople(ctx, input.Query, limit)
			}
			if err != nil {
				return nil, nil, err
			}
			out := map[string]any{"query": input.Query, keyType: searchType, "count": len(results), "results": results}
			if cr, spilled := handleSpill(ctx, platLinkedIn, out); spilled {
				return cr, nil, nil
			}
			return nil, out, nil
		case linkedInOpPosts:
			if input.Handle == "" {
				return nil, nil, errors.New("handle is required for op=posts")
			}
			limit := input.Limit
			if limit <= 0 {
				limit = 10
			}
			posts, err := jobs.VoyagerPosts(ctx, input.Handle, limit)
			if err != nil {
				return nil, nil, err
			}
			return nil, map[string]any{"handle": input.Handle, "count": len(posts), "posts": posts}, nil
		case linkedInOpRating:
			if input.Handle == "" {
				return nil, nil, errors.New("handle is required for op=rating")
			}
			rating, err := jobs.VoyagerRating(ctx, input.Handle)
			if err != nil {
				return nil, nil, err
			}
			return nil, rating, nil
		default:
			return nil, nil, fmt.Errorf("unknown op %q: must be one of profile, company, jobs, search, posts, rating", input.Op)
		}
	})
}
