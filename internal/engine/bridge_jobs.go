package engine

// bridge_jobs.go provides job-specific bridge functions that don't belong in generic bridge.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/anatolykoptev/go-engine/llm"
	"github.com/anatolykoptev/go-engine/metrics"
)

// llmJobOutput is the JSON structure expected from the LLM for job search.
type llmJobOutput struct {
	Jobs    []JobListing `json:"jobs"`
	Summary string       `json:"summary"`
}

// jobSearchTokensPerRecord is the estimated output tokens for one fully-
// populated JobListing JSON record (title, company, url, source, location,
// salary_min/max/currency/interval, job_type, remote, experience, skills[],
// description, posted). 300 is a generous ceiling for the structured fields;
// models that add reasoning or verbose descriptions are covered by the
// salvage path (parseJobSearchResponse) as a safety net.
const jobSearchTokensPerRecord = 300

// jobSearchSummaryBudget reserves output tokens for the "summary" field.
const jobSearchSummaryBudget = 500

// jobSearchMaxOutputTokens computes the output token budget for a job-search
// extraction call. The documented max limit is 50 records (types_jobs.go
// JobSearchInput.Limit). Arithmetic: 50 × 300 + 500 = 15500. We never go
// below the configured LLM_MAX_TOKENS default (16384) so existing deployments
// that already work are not regressed. The salvage path handles any residual
// truncation from models that exceed the per-record estimate.
func jobSearchMaxOutputTokens(numResults int) int {
	needed := numResults*jobSearchTokensPerRecord + jobSearchSummaryBudget
	if cfg.LLMMaxTokens > needed {
		return cfg.LLMMaxTokens
	}
	return needed
}

// SummarizeJobResults calls the LLM with job-specific prompt and parses
// structured job listings. A truncated LLM response is NEVER turned into a
// silent empty result: complete records are salvaged from the truncated JSON
// array via a json.Decoder loop, the truncated tail is dropped with a count,
// and a bounded-label counter (job_search_extraction_total{outcome}) records
// the outcome. The raw JSON is never stuffed into Summary with Jobs left nil.
//
// finish_reason is NOT surfaced by the go-engine LLM client (Complete returns
// only (string, error) — see vendor/.../llm/client.go:369), so truncation is
// detected by the JSON parse failure itself: if json.Unmarshal of the full
// response fails, the response is malformed/truncated and the salvage path
// runs.
func SummarizeJobResults(ctx context.Context, query, instruction string, contentLimit int, results []SearxngResult, contents map[string]string) (*JobSearchOutput, error) {
	sources := llm.BuildSourcesText(results, contents, contentLimit, defaultCharsPerToken)
	prompt := fmt.Sprintf("%s\n\nQuery: %s\n\nSources:\n%s", instruction, query, sources)

	maxOut := jobSearchMaxOutputTokens(len(results))
	raw, err := llmInst.CompleteParams(ctx, prompt, cfg.LLMTemperature, maxOut)
	if err != nil {
		return nil, err
	}

	jobs, summary, outcome, salvaged, dropped := parseJobSearchResponse(raw)
	IncrJobSearchExtraction(outcome)

	if outcome == ExtractionTruncatedSalvaged {
		slog.Warn("job_search: LLM response truncated, salvaged complete records",
			slog.Int("salvaged", salvaged),
			slog.Int("dropped", dropped),
			slog.Int("raw_len", len(raw)))
	}

	// Backfill URLs from positional results (same logic as before).
	for i := range jobs {
		if jobs[i].URL == "" && i < len(results) {
			jobs[i].URL = results[i].URL
		}
	}

	return &JobSearchOutput{Query: query, Jobs: jobs, Summary: summary}, nil
}

// parseJobSearchResponse parses an LLM JSON response into job listings,
// salvaging complete records from a truncated response. Returns
// (jobs, summary, outcome, salvaged, dropped).
//
//   - ok                 — full JSON parsed cleanly; salvaged = len(jobs), dropped = 0
//   - truncated_salvaged — full parse failed; complete array elements decoded
//     via json.Decoder; dropped ≥ 1 (the record being decoded at the truncation
//     point; the true loss may be greater but is unknowable from a truncated stream)
//   - unparseable        — no complete records could be salvaged
//
// This is a pure function (no LLM, no metrics) so it is directly testable with
// canned strings — the falsification tests feed cut-mid-record JSON here.
func parseJobSearchResponse(raw string) (jobs []JobListing, summary string, outcome string, salvaged int, dropped int) {
	var parsed llmJobOutput
	if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
		return parsed.Jobs, parsed.Summary, ExtractionOK, len(parsed.Jobs), 0
	}

	// Full parse failed — the response is malformed or truncated. Salvage
	// complete records from the "jobs" array using a streaming decoder.
	salvagedJobs, salSummary, n := salvageJobs(raw)
	if n > 0 {
		summary = salSummary
		if summary == "" {
			summary = fmt.Sprintf("LLM response truncated; %d complete job records salvaged, at least 1 lost.", n)
		}
		return salvagedJobs, summary, ExtractionTruncatedSalvaged, n, 1
	}

	// Could not salvage any complete records.
	return nil, "LLM response could not be parsed; no job records extracted.", ExtractionUnparseable, 0, 0
}

// salvageJobs extracts complete JobListing records from a (possibly
// truncated) JSON response by streaming the "jobs" array with a json.Decoder.
// Each array element that decodes fully is kept; the decoder stops at the
// first error (the truncation point). Returns (jobs, summary, count).
//
// Does NOT fabricate closing brackets — a truncated array's first N elements
// parse clean, and we explicitly stop at the first failure rather than
// guessing how many more were intended. This avoids the documented trap where
// "finished with 8" and "truncated at 8 of 15" become indistinguishable: the
// outcome counter (truncated_salvaged) makes the distinction visible.
func salvageJobs(raw string) (jobs []JobListing, summary string, count int) {
	// Find the "jobs" array start.
	jobsIdx := strings.Index(raw, `"jobs"`)
	if jobsIdx < 0 {
		return nil, "", 0
	}
	rest := raw[jobsIdx:]
	bracketIdx := strings.Index(rest, "[")
	if bracketIdx < 0 {
		return nil, "", 0
	}

	dec := json.NewDecoder(strings.NewReader(rest[bracketIdx:]))

	// Consume the opening '['.
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('[') {
		return nil, "", 0
	}

	for {
		var job JobListing
		if err := dec.Decode(&job); err != nil {
			// io.EOF = clean end of array (no truncation, but the outer
			// object was malformed in some other way — e.g. missing summary).
			// Any other error = truncated mid-record.
			break
		}
		jobs = append(jobs, job)
	}

	// Try to salvage the summary field — it may appear before or after the
	// jobs array. Search for it in the raw string.
	summary = extractSummaryField(raw)

	return jobs, summary, len(jobs)
}

// extractSummaryField attempts to extract the "summary" string field from a
// possibly-truncated JSON response. Returns "" if not found.
func extractSummaryField(raw string) string {
	idx := strings.Index(raw, `"summary"`)
	if idx < 0 {
		return ""
	}
	rest := raw[idx+len(`"summary"`):]
	rest = strings.TrimSpace(rest)
	if len(rest) == 0 || rest[0] != ':' {
		return ""
	}
	rest = strings.TrimSpace(rest[1:])
	if len(rest) == 0 || rest[0] != '"' {
		return ""
	}
	// Find the closing quote, respecting escaped characters.
	rest = rest[1:]
	var sb strings.Builder
	for i := 0; i < len(rest); i++ {
		if rest[i] == '\\' && i+1 < len(rest) {
			switch rest[i+1] {
			case '"':
				sb.WriteByte('"')
				i++
				continue
			case 'n':
				sb.WriteByte('\n')
				i++
				continue
			case '\\':
				sb.WriteByte('\\')
				i++
				continue
			}
		}
		if rest[i] == '"' {
			return sb.String()
		}
		sb.WriteByte(rest[i])
	}
	return sb.String()
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
