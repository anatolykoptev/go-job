package jobserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// AlgoraJobIngestInput is the input for the algora_job_ingest tool.
type AlgoraJobIngestInput struct {
	URL string `json:"url,omitempty" jsonschema:"Single Algora job URL (e.g. https://algora.io/comfy/job/cz9bpQrBC38UDigM). Provide url OR org, not both."`
	Org string `json:"org,omitempty" jsonschema:"Algora org slug (e.g. comfy). Fetches all active jobs for the org. Provide url OR org, not both."`
}

// AlgoraJobIngestResult is the output for the algora_job_ingest tool.
type AlgoraJobIngestResult struct {
	Ingested int      `json:"ingested"`
	URLs     []string `json:"urls,omitempty"`
	Summary  string   `json:"summary"`
}

// validateAlgoraJobURL parses rawURL and verifies it belongs to algora.io.
// Returns the parsed URL or an error.
func validateAlgoraJobURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Hostname() != "algora.io" {
		return nil, fmt.Errorf("URL must be on algora.io, got: %s", parsed.Hostname())
	}
	return parsed, nil
}

func registerAlgoraJobIngest(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "algora_job_ingest",
		Description: "Ingest Algora job posting(s) into the hunt store. " +
			"Provide url (single job) or org (all active jobs for the org). " +
			"Source is always 'algora-jobs'. Returns upserted count + URLs.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input AlgoraJobIngestInput) (*mcp.CallToolResult, *AlgoraJobIngestResult, error) {
		if input.URL == "" && input.Org == "" {
			return nil, nil, errors.New("algora_job_ingest: provide url or org")
		}
		if input.URL != "" && input.Org != "" {
			return nil, nil, errors.New("algora_job_ingest: provide url OR org, not both")
		}

		var listings []engine.JobListing

		if input.URL != "" {
			// Validate host and path before passing to FetchAlgoraJob.
			parsed, urlErr := validateAlgoraJobURL(input.URL)
			if urlErr != nil {
				return nil, nil, fmt.Errorf("algora_job_ingest: URL must be on algora.io: %s", input.URL)
			}
			if !strings.Contains(parsed.Path, "/job/") {
				return nil, nil, fmt.Errorf("algora_job_ingest: not a single-job URL: %s", input.URL)
			}
			listing, err := jobs.FetchAlgoraJob(ctx, input.URL)
			if err != nil {
				slog.Warn("algora_job_ingest: fetch error", slog.Any("error", err))
				return nil, &AlgoraJobIngestResult{
					Ingested: 0,
					Summary:  fmt.Sprintf("fetch error: %v", err),
				}, nil
			}
			if listing != nil {
				listings = append(listings, *listing)
			}
		} else {
			var err error
			listings, err = jobs.DiscoverAlgoraOrgJobs(ctx, input.Org)
			if err != nil {
				slog.Warn("algora_job_ingest: discovery error", slog.Any("error", err))
				return nil, &AlgoraJobIngestResult{
					Ingested: 0,
					Summary:  fmt.Sprintf("discovery error: %v", err),
				}, nil
			}
		}

		// Persist to hunt store (best-effort, mirrors persistJobListings).
		persistJobListings(ctx, listings)

		urls := make([]string, 0, len(listings))
		for _, l := range listings {
			urls = append(urls, l.URL)
		}

		return nil, &AlgoraJobIngestResult{
			Ingested: len(listings),
			URLs:     urls,
			Summary:  fmt.Sprintf("ingested %d algora-jobs listing(s)", len(listings)),
		}, nil
	})
}
