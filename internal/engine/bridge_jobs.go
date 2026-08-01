package engine

// bridge_jobs.go provides job-specific bridge functions that don't belong in generic bridge.go.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/anatolykoptev/go-engine/metrics"
)

// llmJobOutput is the JSON structure expected from the LLM for job search.
type llmJobOutput struct {
	Jobs    []JobListing `json:"jobs"`
	Summary string       `json:"summary"`
}

// summarizeJobResultsLLM is the seam over the LLM call in SummarizeJobResults.
// Defaults to the real SummarizeToJSON; tests swap it to inject canned
// unparseable responses without a live LLM.
//
//nolint:gochecknoglobals // test seam, defaults to the real implementation
var summarizeJobResultsLLM = SummarizeToJSON[llmJobOutput]

// SummarizeJobResults calls the LLM with job-specific prompt and parses structured job listings.
func SummarizeJobResults(ctx context.Context, query, instruction string, contentLimit int, results []SearxngResult, contents map[string]string) (*JobSearchOutput, error) {
	parsed, raw, err := summarizeJobResultsLLM(ctx, query, instruction, contentLimit, results, contents)
	if err != nil {
		return nil, err
	}
	if parsed == nil {
		// LLM returned a response that could not be parsed as the expected
		// JSON — typically a truncated output (mid-record cut, output cap
		// hit, stream abort). The raw text must NOT reach the caller (it is
		// indistinguishable from a real job listing at a glance and was the
		// silent-failure surface in #413). Return an honest message stating
		// the response was incomplete and how many sources were collected.
		// Count it and log the two numbers that identify the cause:
		//   - raw_len: byte length of the model output. If raw_len/4 is near
		//     the output token budget, the cause is the output cap; if it
		//     sits far below, the cut is upstream (proxy, stream abort, or a
		//     specific routed model).
		//   - model + model_weights: which model served the request (nine
		//     models are weight-routed via LLM_MODEL_WEIGHTS, so the routed
		//     model is not known today — log the configured model and the
		//     full weights env so the operator can narrow it).
		IncrJobSearchExtraction("unparseable")
		slog.Warn("job_search: LLM response unparseable, returning honest empty",
			slog.Int("raw_len", len(raw)),
			slog.String("model", cfg.LLMModel),
			slog.String("model_weights", os.Getenv("LLM_MODEL_WEIGHTS")),
			slog.Int("sources", len(results)),
		)
		return &JobSearchOutput{
			Query:   query,
			Summary: fmt.Sprintf("LLM response was incomplete/unparseable; %d source(s) collected but no job listings extracted.", len(results)),
		}, nil
	}
	IncrJobSearchExtraction("ok")

	for i := range parsed.Jobs {
		if parsed.Jobs[i].URL == "" && i < len(results) {
			parsed.Jobs[i].URL = results[i].URL
		}
	}
	return &JobSearchOutput{Query: query, Jobs: parsed.Jobs, Summary: parsed.Summary}, nil
}

// FetchContentsParallel fetches text content from URLs in parallel.
// URLs present in skipURLs are skipped. Pass nil to fetch all.
func FetchContentsParallel(ctx context.Context, results []SearxngResult, skipURLs map[string]bool) map[string]string {
	contents := make(map[string]string, len(results))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, r := range results {
		if skipURLs[r.URL] {
			continue
		}
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			_, text, err := FetchURLContent(ctx, u)
			if err == nil && text != "" {
				mu.Lock()
				contents[u] = text
				mu.Unlock()
			}
		}(r.URL)
	}
	wg.Wait()
	return contents
}

// CanonicalJobKey returns a normalized dedup key for cross-source job deduplication.
func CanonicalJobKey(title, location string) string {
	norm := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		if idx := strings.LastIndex(s, " at "); idx > 0 {
			s = s[:idx]
		}
		var b strings.Builder
		prevSpace := true
		for _, r := range s {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
				prevSpace = false
			} else if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		}
		return strings.TrimRight(b.String(), " ")
	}
	return norm(title) + "|" + norm(location)
}

// TrackOperation delegates to go-engine metrics.TrackOperation which logs
// a warning when fn takes longer than the configured threshold.
func TrackOperation(ctx context.Context, name string, fn func(context.Context) error) error {
	return metrics.TrackOperation(ctx, name, fn)
}
