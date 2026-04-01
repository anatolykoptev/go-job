package jobserver

import (
	"context"
	"errors"

	linkedin "github.com/anatolykoptev/go-linkedin"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// --- linkedin_profile ---

type linkedInProfileInput struct {
	Handle string `json:"handle" jsonschema:"LinkedIn handle (e.g. 'koptev') or full profile URL"`
}

func registerLinkedInProfile(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "linkedin_profile",
		Description: "Full LinkedIn profile by handle or URL. Returns experience, education, skills, contact info.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input linkedInProfileInput) (*mcp.CallToolResult, *linkedin.Profile, error) {
		if input.Handle == "" {
			return nil, nil, errors.New("handle is required")
		}
		profile, err := jobs.VoyagerProfile(ctx, input.Handle)
		if err != nil {
			return nil, nil, err
		}
		return nil, profile, nil
	})
}

// --- linkedin_company ---

type linkedInCompanyInput struct {
	Company string `json:"company" jsonschema:"Company slug (e.g. 'hightouch') or full company URL"`
}

func registerLinkedInCompany(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "linkedin_company",
		Description: "LinkedIn company page. Returns description, size, industry, headquarters, specialties.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input linkedInCompanyInput) (*mcp.CallToolResult, *linkedin.Company, error) {
		if input.Company == "" {
			return nil, nil, errors.New("company is required")
		}
		company, err := jobs.VoyagerCompany(ctx, input.Company)
		if err != nil {
			return nil, nil, err
		}
		return nil, company, nil
	})
}

// --- linkedin_jobs ---

type linkedInJobsInput struct {
	Query    string `json:"query" jsonschema:"Job search keywords"`
	Location string `json:"location,omitempty" jsonschema:"Location filter (optional)"`
	Remote   string `json:"remote,omitempty" jsonschema:"Work type: remote, hybrid, onsite (optional)"`
	Limit    int    `json:"limit,omitempty" jsonschema:"Max results (default 10, max 25)"`
}

type linkedInJobsOutput struct {
	Query string         `json:"query"`
	Count int            `json:"count"`
	Jobs  []linkedin.Job `json:"jobs"`
}

func registerLinkedInJobs(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "linkedin_jobs",
		Description: "Search LinkedIn job listings via Voyager API (authenticated). Requires LinkedIn credentials.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input linkedInJobsInput) (*mcp.CallToolResult, linkedInJobsOutput, error) {
		if input.Query == "" {
			return nil, linkedInJobsOutput{}, errors.New("query is required")
		}
		if input.Limit <= 0 {
			input.Limit = 10
		}
		if input.Limit > 25 {
			input.Limit = 25
		}
		result, err := jobs.VoyagerJobs(ctx, linkedin.JobSearchParams{
			Query:    input.Query,
			Location: input.Location,
			Remote:   input.Remote,
			Limit:    input.Limit,
		})
		if err != nil {
			return nil, linkedInJobsOutput{}, err
		}
		return nil, linkedInJobsOutput{Query: input.Query, Count: len(result), Jobs: result}, nil
	})
}

// --- linkedin_search ---

type linkedInSearchInput struct {
	Query      string `json:"query" jsonschema:"Search keywords"`
	SearchType string `json:"type,omitempty" jsonschema:"Search type: people (default) or companies"`
	Limit      int    `json:"limit,omitempty" jsonschema:"Max results (default 10)"`
}

type linkedInSearchOutput struct {
	Query   string                  `json:"query"`
	Type    string                  `json:"type"`
	Count   int                     `json:"count"`
	Results []linkedin.SearchResult `json:"results"`
}

func registerLinkedInSearch(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "linkedin_search",
		Description: "Search LinkedIn for people or companies via Voyager API.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input linkedInSearchInput) (*mcp.CallToolResult, linkedInSearchOutput, error) {
		if input.Query == "" {
			return nil, linkedInSearchOutput{}, errors.New("query is required")
		}
		if input.Limit <= 0 {
			input.Limit = 10
		}
		searchType := "people"
		if input.SearchType == "companies" {
			searchType = "companies"
		}
		var results []linkedin.SearchResult
		var err error
		if searchType == "companies" {
			results, err = jobs.VoyagerSearchCompanies(ctx, input.Query, input.Limit)
		} else {
			results, err = jobs.VoyagerSearchPeople(ctx, input.Query, input.Limit)
		}
		if err != nil {
			return nil, linkedInSearchOutput{}, err
		}
		return nil, linkedInSearchOutput{Query: input.Query, Type: searchType, Count: len(results), Results: results}, nil
	})
}

// --- linkedin_posts ---

type linkedInPostsInput struct {
	Handle string `json:"handle" jsonschema:"LinkedIn handle"`
	Limit  int    `json:"limit,omitempty" jsonschema:"Max posts (default 10)"`
}

type linkedInPostsOutput struct {
	Handle string          `json:"handle"`
	Count  int             `json:"count"`
	Posts  []linkedin.Post `json:"posts"`
}

func registerLinkedInPosts(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "linkedin_posts",
		Description: "Get profile posts with engagement metrics (likes, comments, reposts).",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input linkedInPostsInput) (*mcp.CallToolResult, linkedInPostsOutput, error) {
		if input.Handle == "" {
			return nil, linkedInPostsOutput{}, errors.New("handle is required")
		}
		if input.Limit <= 0 {
			input.Limit = 10
		}
		posts, err := jobs.VoyagerPosts(ctx, input.Handle, input.Limit)
		if err != nil {
			return nil, linkedInPostsOutput{}, err
		}
		return nil, linkedInPostsOutput{Handle: input.Handle, Count: len(posts), Posts: posts}, nil
	})
}

// --- linkedin_rating ---

type linkedInRatingInput struct {
	Handle string `json:"handle" jsonschema:"LinkedIn handle"`
}

func registerLinkedInRating(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "linkedin_rating",
		Description: "Computed profile rating: influence score, completeness, engagement metrics.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input linkedInRatingInput) (*mcp.CallToolResult, *linkedin.ProfileRating, error) {
		if input.Handle == "" {
			return nil, nil, errors.New("handle is required")
		}
		rating, err := jobs.VoyagerRating(ctx, input.Handle)
		if err != nil {
			return nil, nil, err
		}
		return nil, rating, nil
	})
}
