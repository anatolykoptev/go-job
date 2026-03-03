package engine

// bridge_jobs.go provides job-specific bridge functions that don't belong in generic bridge.go.

import (
	"context"
	"strings"
	"sync"

	"github.com/anatolykoptev/go-engine/metrics"
)

// llmJobOutput is the JSON structure expected from the LLM for job search.
type llmJobOutput struct {
	Jobs    []JobListing `json:"jobs"`
	Summary string       `json:"summary"`
}

// llmFreelanceOutput is the JSON structure expected from the LLM for freelance search.
type llmFreelanceOutput struct {
	Projects []FreelanceProject `json:"projects"`
	Summary  string             `json:"summary"`
}

// SummarizeJobResults calls the LLM with job-specific prompt and parses structured job listings.
func SummarizeJobResults(ctx context.Context, query, instruction string, contentLimit int, results []SearxngResult, contents map[string]string) (*JobSearchOutput, error) {
	parsed, raw, err := SummarizeToJSON[llmJobOutput](ctx, query, instruction, contentLimit, results, contents)
	if err != nil {
		return nil, err
	}
	if parsed == nil {
		return &JobSearchOutput{Query: query, Summary: raw}, nil
	}

	for i := range parsed.Jobs {
		if parsed.Jobs[i].URL == "" && i < len(results) {
			parsed.Jobs[i].URL = results[i].URL
		}
	}
	return &JobSearchOutput{Query: query, Jobs: parsed.Jobs, Summary: parsed.Summary}, nil
}

// SummarizeFreelanceResults calls the LLM with freelance-specific prompt and parses structured projects.
func SummarizeFreelanceResults(ctx context.Context, query, instruction string, contentLimit int, results []SearxngResult, contents map[string]string) (*FreelanceSearchOutput, error) {
	parsed, raw, err := SummarizeToJSON[llmFreelanceOutput](ctx, query, instruction, contentLimit, results, contents)
	if err != nil {
		return nil, err
	}
	if parsed == nil {
		return &FreelanceSearchOutput{Query: query, Summary: raw}, nil
	}

	for i := range parsed.Projects {
		if parsed.Projects[i].URL == "" && i < len(results) {
			parsed.Projects[i].URL = results[i].URL
		}
	}
	return &FreelanceSearchOutput{Query: query, Projects: parsed.Projects, Summary: parsed.Summary}, nil
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
