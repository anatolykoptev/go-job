package jobserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// VacancyIngestInput is the input schema for the vacancy_ingest tool.
// persist is a string enum ("true"/"false") rather than a bool because the
// go-sdk reflects `bool`-typed fields in output structs as boolean true in the
// JSON Schema, which the MCP client rejects (TestNoBooleanPropertySchema).
// For the INPUT schema this is fine with a plain bool, but we use a string here
// to be consistent with the no-boolean-property pattern and keep the schema
// self-documenting.
type VacancyIngestInput struct {
	URL     string `json:"url"               jsonschema:"Required. Full URL of the job-posting page to fetch and ingest."`
	Source  string `json:"source,omitempty"  jsonschema:"Optional source hint (e.g. 'greenhouse', 'lever'). Defaults to URL-derived source."`
	Company string `json:"company,omitempty" jsonschema:"Optional company name hint. Used when LLM extraction misses it."`
	Persist string `json:"persist,omitempty" jsonschema:"Whether to persist the extracted job into the hunt store. Values: 'true' (default) or 'false'."`
}

// VacancyIngestResult is the output schema for the vacancy_ingest tool.
type VacancyIngestResult struct {
	Job            engine.JobListing `json:"job"`
	Outcome        string            `json:"outcome"`
	HuntID         int64             `json:"hunt_id,omitempty"`
	ExtractQuality string            `json:"extract_quality"`
	Source         string            `json:"source"`
}

func registerVacancyIngest(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "vacancy_ingest",
		Description: "Fetch a single job-posting page by URL via the go-wowa stealth render, extract structured job details " +
			"with the shared LLM extractor, and optionally persist the result into the hunt store. " +
			"Returns the extracted job, persist outcome (created/merged/skipped), and extract_quality (ok/weak). " +
			"Idempotent: ingesting the same URL twice returns outcome=merged.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input VacancyIngestInput) (*mcp.CallToolResult, *VacancyIngestResult, error) {
		if input.URL == "" {
			return nil, nil, errors.New("vacancy_ingest: url is required")
		}

		// Resolve persist flag — default true.
		persist := input.Persist != "false"

		// Fetch + LLM-extract the vacancy page.
		job, extractQuality, err := jobs.FetchVacancy(ctx, input.URL, input.Source, input.Company)
		if err != nil {
			slog.Warn("vacancy_ingest: fetch failed", slog.String("url", input.URL), slog.Any("error", err))
			return nil, &VacancyIngestResult{
				Outcome:        "fetch_error",
				ExtractQuality: "",
				Source:         input.Source,
			}, fmt.Errorf("vacancy_ingest: %w", err)
		}

		result := &VacancyIngestResult{
			Job:            job,
			ExtractQuality: extractQuality,
			Source:         job.Source,
		}

		// Increment dedicated counter regardless of persist path.
		engine.IncrVacancyIngest(extractQuality)

		if !persist {
			result.Outcome = "skipped"
			return nil, result, nil
		}

		// Persist path — guard nil store loudly.
		store := engine.GetHuntStore()
		if store == nil {
			slog.Warn("vacancy_ingest: hunt store nil, persist skipped", slog.String("url", input.URL))
			engine.IncrVacancyIngestSkipped()
			result.Outcome = "skipped"
			return nil, result, nil
		}

		hj := jobs.JobListingToHunt(job)
		id, outcome, upsertErr := store.UpsertJob(ctx, hj)
		engine.IncrHuntIngest(hunt.KindJob, outcome.String())
		if upsertErr != nil {
			slog.Warn("vacancy_ingest: upsert failed", slog.String("url", input.URL), slog.Any("error", upsertErr))
			result.Outcome = "error"
			return nil, result, nil
		}

		result.HuntID = id
		result.Outcome = outcome.String()
		return nil, result, nil
	})
}
