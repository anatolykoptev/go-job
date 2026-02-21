package jobserver

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/anatolykoptev/go_job/internal/toolutil"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type TwitterJobSearchInput struct {
	Query    string `json:"query" jsonschema:"Job search keywords (e.g. golang developer, hiring react)"`
	Limit    int    `json:"limit,omitempty" jsonschema:"Max tweets to return (default 20, max 50)"`
	Language string `json:"language,omitempty" jsonschema:"Language code (default: all)"`
}

type TwitterJobSearchOutput struct {
	Query  string               `json:"query"`
	Count  int                  `json:"count"`
	Tweets []jobs.TwitterJobTweet `json:"tweets"`
}

func registerTwitterJobSearch(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "twitter_job_search",
		Description: "Search Twitter/X for job postings and hiring tweets. Returns raw tweets from recruiters and companies posting job openings (#hiring, we're hiring, etc.). Fast — no LLM processing, returns tweet data directly.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input TwitterJobSearchInput) (*mcp.CallToolResult, TwitterJobSearchOutput, error) {
		if input.Query == "" {
			return nil, TwitterJobSearchOutput{}, fmt.Errorf("query is required")
		}

		limit := input.Limit
		if limit <= 0 {
			limit = 20
		}
		if limit > 50 {
			limit = 50
		}

		cacheKey := engine.CacheKey("twitter_job_search", input.Query, fmt.Sprintf("%d", limit))
		if out, ok := toolutil.CacheLoadJSON[TwitterJobSearchOutput](ctx, cacheKey); ok {
			return nil, out, nil
		}

		tweets, err := jobs.SearchTwitterJobsRaw(ctx, input.Query, limit)
		if err != nil {
			slog.Warn("twitter_job_search error", slog.Any("error", err))
			return nil, TwitterJobSearchOutput{}, fmt.Errorf("twitter search failed: %w", err)
		}

		out := TwitterJobSearchOutput{
			Query:  input.Query,
			Count:  len(tweets),
			Tweets: tweets,
		}

		toolutil.CacheStoreJSON(ctx, cacheKey, input.Query, out)
		return nil, out, nil
	})
}
