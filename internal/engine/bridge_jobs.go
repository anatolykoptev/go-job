package engine

// bridge_jobs.go provides job-specific bridge functions that don't belong in generic bridge.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
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

// jobSearchComplete is the LLM completion seam for SummarizeJobResults.
// Defaults to llmInst.CompleteParams; tests swap it to inject canned
// responses without touching production structure.
var jobSearchComplete = func(ctx context.Context, prompt string, temperature float64, maxTokens int, opts ...llm.ChatOption) (string, error) {
	return llmInst.CompleteParams(ctx, prompt, temperature, maxTokens, opts...)
}

// SummarizeJobResults calls the LLM with job-specific prompt and parses
// structured job listings. A truncated LLM response is NEVER turned into a
// silent empty result: complete records are salvaged from the truncated JSON
// array via a json.Decoder loop, the truncated tail is dropped with a count,
// and a bounded-label counter (job_search_extraction_total{outcome}) records
// the outcome. The raw JSON is never stuffed into Summary with Jobs left nil.
//
// Reasoning tokens are disabled (WithReasoningEffort("none")) because the
// routed model bounds max_tokens on the COMPLETION including thinking tokens —
// reasoning can consume the entire output budget and cut the JSON mid-record
// even when the record count is well within the token ceiling. Every other
// go-engine LLM path (query.go, summarize.go) already does this; this path
// was the lone exception.
//
// finish_reason is NOT surfaced by the go-engine LLM client (Complete returns
// only (string, error) — see vendor/.../llm/client.go:369), so truncation is
// detected by the JSON parse failure itself: if json.Unmarshal of the full
// response fails, the response is malformed/truncated and the salvage path
// runs.
//
// URL backfill is NOT done here. The caller (runJobSearch) owns the
// SearxngResult→JobListing correspondence and applies it via
// assignFallbackURLs, which guards on equal lengths. A salvage path that
// drops records breaks the positional correspondence by construction, so
// guessing URLs here would mis-assign another listing's URL to a salvaged
// record. FetchVacancy has its own URL backfill at the call site.
func SummarizeJobResults(ctx context.Context, query, instruction string, contentLimit int, results []SearxngResult, contents map[string]string) (*JobSearchOutput, error) {
	sources := BuildSourcesText(results, contents, contentLimit)
	prompt := fmt.Sprintf("%s\n\nQuery: %s\n\nSources:\n%s", instruction, query, sources)

	raw, err := jobSearchComplete(ctx, prompt, cfg.LLMTemperature, cfg.LLMMaxTokens, llm.WithReasoningEffort("none"))
	if err != nil {
		return nil, err
	}

	jobs, summary, outcome, salvaged, dropped := parseJobSearchResponse(raw)
	IncrJobSearchExtraction(outcome)

	if outcome != ExtractionOK && outcome != ExtractionTrailingGarbage {
		// The warn line carries raw_len + the configured model candidate set
		// (LLM_MODEL_WEIGHTS, bounded at 9 values) + the primary model. If
		// raw_len/4 ≈ LLMMaxTokens the cut is the token cap; if raw_len sits
		// far below the ceiling, the cut is upstream (proxy, stream abort, or
		// a weight-routed model) and no amount of WithReasoningEffort will
		// touch it. That one field separates the two hypotheses on the first
		// production occurrence, with no new instrumentation.
		slog.Warn("job_search: LLM response parse issue, salvaged complete records",
			slog.String("outcome", outcome),
			slog.Int("salvaged", salvaged),
			slog.Int("dropped", dropped),
			slog.Int("raw_len", len(raw)),
			slog.String("llm_model", cfg.LLMModel),
			slog.String("llm_model_weights", os.Getenv("LLM_MODEL_WEIGHTS")))
	}

	return &JobSearchOutput{Query: query, Jobs: jobs, Summary: summary}, nil
}

// parseJobSearchResponse parses an LLM JSON response into job listings,
// salvaging complete records from a truncated response. Returns
// (jobs, summary, outcome, salvaged, dropped).
//
//   - ok                 — full JSON parsed cleanly; salvaged = len(jobs), dropped = 0
//   - trailing_garbage   — full parse failed (e.g. trailing prose after the
//     JSON object) but the "jobs" array AND enclosing object closed cleanly;
//     all decoded records are kept, dropped = 0. A chat-tuned model that
//     appends "Hope this helps!" lands here, NOT in truncated_salvaged.
//   - schema_mismatch    — the array closed cleanly but one or more elements
//     failed to unmarshal into JobListing (e.g. "salary_min":"160000" — a
//     string where *int is declared). The bad elements were skipped and the
//     rest kept; dropped = exact count of skipped elements. This is NOT
//     truncation — the array was complete, the model just sent a wrong type.
//   - truncated_salvaged — the "jobs" array did NOT close cleanly (cut
//     mid-record by the output token budget) OR the enclosing object was
//     truncated (e.g. the summary field was cut mid-value); complete elements
//     decoded before the cut are kept; dropped = schema skips + at least 1
//     for the truncated tail (the true loss may be greater but is unknowable
//     from a truncated stream)
//   - unparseable        — no "jobs" array found, or no complete record decoded
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
	salvagedJobs, salSummary, n, arrayClosed, objectClosed, schemaDropped := salvageJobs(raw)

	if !arrayClosed {
		// Array did not close cleanly — genuine mid-record truncation.
		if n > 0 {
			summary = salSummary
			if summary == "" {
				summary = fmt.Sprintf("LLM response truncated; %d complete job records salvaged, at least 1 lost.", n)
			}
			return salvagedJobs, summary, ExtractionTruncatedSalvaged, n, schemaDropped + 1
		}
		return nil, "LLM response could not be parsed; no job records extracted.", ExtractionUnparseable, 0, 0
	}

	// Array closed cleanly. Now distinguish:
	// (a) enclosing object also closed → healthy (trailing_garbage or schema_mismatch)
	// (b) enclosing object truncated → truncated_salvaged (the response WAS
	//     cut, just outside the array — e.g. the summary field was cut
	//     mid-value; an empty summary must never reach the user unmarked)
	if !objectClosed {
		if n > 0 {
			summary = salSummary
			if summary == "" {
				summary = fmt.Sprintf("LLM response truncated; %d complete job records salvaged, summary was cut.", n)
			}
			return salvagedJobs, summary, ExtractionTruncatedSalvaged, n, schemaDropped
		}
		return nil, "LLM response could not be parsed; no job records extracted.", ExtractionUnparseable, 0, 0
	}

	// Array closed, object closed — healthy salvage. Distinguish schema
	// mismatch (some elements skipped) from clean trailing garbage.
	if schemaDropped > 0 {
		return salvagedJobs, salSummary, ExtractionSchemaMismatch, n, schemaDropped
	}
	// Preserve the model's summary (even for an empty array: a legitimate
	// "none match" explanation must not be replaced by a parse-error message).
	return salvagedJobs, salSummary, ExtractionTrailingGarbage, n, 0
}

// schemaPlaceholderSummary is the exact summary text from the prompt's example
// JSON object. A model that restates the format before answering puts this
// string where the real summary should be; extractSummaryField skips it.
const schemaPlaceholderSummary = "1-2 sentence recommendation: which jobs look most promising and why, or an honest statement that none match the query"

// isSchemaPlaceholderJob reports whether a decoded JobListing matches the
// prompt's example object — a model that restates the format before answering
// produces a record whose Title and Company are the literal placeholder values
// ("job title", "company name"). A real listing never carries both.
func isSchemaPlaceholderJob(job JobListing) bool {
	return job.Title == "job title" && job.Company == "company name"
}

// salvageResult holds the output of decoding a single "jobs" array candidate.
type salvageResult struct {
	jobs          []JobListing
	arrayClosed   bool
	objectClosed  bool
	schemaDropped int
	objStart      int // enclosing object '{' offset, -1 if bare array
	objEnd        int // enclosing object '}' offset + 1 (exclusive), -1 if not closed
	keyIdx        int // "jobs" key offset
	bracketIdx    int // "[" offset
}

// jobsArrayCandidate identifies one "jobs" array occurrence: the "jobs" key
// offset and the "[" offset that opens the array.
type jobsArrayCandidate struct {
	keyIdx     int
	bracketIdx int
}

// betterThan returns true if r yields more real records than other, or equal
// real records with fewer schema drops. Used to pick the winning candidate
// when a model restates the format (placeholder "jobs" array) before the real
// answer.
func (r salvageResult) betterThan(other salvageResult) bool {
	if len(r.jobs) != len(other.jobs) {
		return len(r.jobs) > len(other.jobs)
	}
	return r.schemaDropped < other.schemaDropped
}

// salvageJobs extracts complete JobListing records from a (possibly
// truncated) JSON response by streaming the "jobs" array with a json.Decoder.
// Each array element that decodes fully is kept. Returns (jobs, summary,
// count, arrayClosed, objectClosed, schemaDropped).
//
// BLOCKER 1 fix — candidate scanning: a chat-tuned model may restate the
// format (with a placeholder "jobs" array whose records have Title="job
// title", Company="company name") before the real answer. Anchoring on the
// FIRST "jobs" occurrence returns the placeholder as a real job listing. We
// scan ALL candidate "jobs" array starts and pick the one yielding the most
// non-placeholder records. Placeholder records (matching the prompt's example
// values) are skipped as a belt.
//
// BLOCKER 1 summary fix — bounded extraction: the summary is extracted from
// the SAME object that produced the chosen jobs array, not from the whole raw
// string. A model that paraphrases the echo summary (abbreviating or reflowing
// the placeholder text) defeats the exact-match placeholder check; the
// whole-string scan then returns the paraphrased echo from a different object
// while the jobs come from the real one — jobs and summary disagree. Binding
// the scan to the chosen object's byte bounds makes the defence structural:
// the echo is in a different object and is never reached.
//
// BLOCKER 2 fix — RawMessage decode: array elements are decoded as
// json.RawMessage first (which never fails on a type mismatch), then
// unmarshaled into JobListing independently. A mid-array element with a wrong
// type (e.g. "salary_min":"160000" — string where *int is declared) is SKIPPED
// and scanning continues to ']'; the old code broke out of the loop on the
// first Decode failure, losing every complete record after it and reporting
// dropped=1 (a constant that misattributed the loss to the token budget).
// schemaDropped is the exact count of skipped elements.
//
// MAJOR 3 fix — objectClosed: after the array closes, we check whether the
// enclosing object also closed. A response where the array is complete but
// the summary field is cut mid-value has arrayClosed=true but
// objectClosed=false; the outcome is truncated_salvaged (NOT trailing_garbage)
// and the empty summary is replaced by a synthetic fallback so an empty
// summary never reaches the user unmarked.
func salvageJobs(raw string) (jobs []JobListing, summary string, count int, arrayClosed bool, objectClosed bool, schemaDropped int) {
	candidates := findJobsArrayStarts(raw)
	if len(candidates) == 0 {
		return nil, "", 0, false, false, 0
	}

	results := make([]salvageResult, len(candidates))
	var best salvageResult
	bestIdx := 0
	for i, c := range candidates {
		results[i] = decodeJobsCandidate(raw, c.keyIdx, c.bracketIdx)
		if i == 0 || results[i].betterThan(best) {
			best = results[i]
			bestIdx = i
		}
	}

	// Extract the summary from the SAME object that produced the chosen jobs
	// array — never from elsewhere in the response. This binds jobs and
	// summary to one candidate so a paraphrased echo summary from a different
	// object (e.g. a format restatement) cannot leak in. The schema
	// placeholder check remains as a belt.
	endBound := best.objEnd
	if endBound < 0 {
		// Object not closed (truncated). Bound to the next candidate's
		// enclosing object start to prevent scanning into a different object.
		endBound = len(raw)
		if bestIdx+1 < len(results) && results[bestIdx+1].objStart >= 0 {
			endBound = results[bestIdx+1].objStart
		}
	}
	summary, _ = extractSummaryFromBounds(raw, best.objStart, endBound)

	return best.jobs, summary, len(best.jobs), best.arrayClosed, best.objectClosed, best.schemaDropped
}

// findJobsArrayStarts returns byte offsets in raw of each "[" that opens a
// "jobs" array — i.e. each occurrence of `"jobs"` followed (after optional
// whitespace and a colon) by "[". Each result carries both the "jobs" key
// offset (for enclosing-object bounds) and the "[" offset (for decoder start).
func findJobsArrayStarts(raw string) []jobsArrayCandidate {
	var starts []jobsArrayCandidate
	searchFrom := 0
	for {
		idx := strings.Index(raw[searchFrom:], `"jobs"`)
		if idx < 0 {
			break
		}
		idx += searchFrom
		pos := idx + len(`"jobs"`)
		for pos < len(raw) && isJSONSpace(raw[pos]) {
			pos++
		}
		if pos < len(raw) && raw[pos] == ':' {
			pos++
			for pos < len(raw) && isJSONSpace(raw[pos]) {
				pos++
			}
			if pos < len(raw) && raw[pos] == '[' {
				starts = append(starts, jobsArrayCandidate{keyIdx: idx, bracketIdx: pos})
			}
		}
		searchFrom = idx + len(`"jobs"`)
	}
	return starts
}

func isJSONSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// decodeJobsCandidate streams one "jobs" array candidate starting at
// bracketIdx (the "[" offset in raw) and returns the decoded records plus
// closure signals. keyIdx is the "jobs" key offset, used to compute the
// enclosing object's byte bounds for bounded summary extraction.
func decodeJobsCandidate(raw string, keyIdx, bracketIdx int) salvageResult {
	rest := raw[bracketIdx:]
	dec := json.NewDecoder(strings.NewReader(rest))

	// Consume the opening '['.
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('[') {
		return salvageResult{keyIdx: keyIdx, bracketIdx: bracketIdx, objStart: -1, objEnd: -1}
	}

	r := salvageResult{keyIdx: keyIdx, bracketIdx: bracketIdx, objStart: -1, objEnd: -1}

	for {
		if !dec.More() {
			closer, cerr := dec.Token()
			if cerr == nil && closer == json.Delim(']') {
				r.arrayClosed = true
			}
			break
		}
		// Decode as RawMessage first — never fails on a type mismatch.
		var rawMsg json.RawMessage
		if err := dec.Decode(&rawMsg); err != nil {
			// Mid-element decode failure = truncation (stream cut inside
			// an element). arrayClosed stays false.
			break
		}
		var job JobListing
		if err := json.Unmarshal(rawMsg, &job); err != nil {
			// Schema mismatch (e.g. string where *int expected). Skip this
			// element but KEEP SCANNING — the rest of the array may be fine.
			r.schemaDropped++
			continue
		}
		if isSchemaPlaceholderJob(job) {
			// Schema echo — the model restated the format example as if it
			// were a real record. Skip it.
			r.schemaDropped++
			continue
		}
		r.jobs = append(r.jobs, job)
	}

	// Check whether the enclosing object closed after the array, and capture
	// the '}' offset for bounded summary extraction.
	if r.arrayClosed {
		// dec.InputOffset() gives the byte offset within the stream (which
		// starts at bracketIdx) just past the last token consumed (the ']').
		afterArray := bracketIdx + int(dec.InputOffset())
		closed, closeIdx := checkObjectClosedRaw(raw, afterArray)
		r.objectClosed = closed
		if closed {
			r.objEnd = closeIdx + 1
		}
	}

	// Compute the enclosing object's '{' offset for bounded summary extraction.
	r.objStart = findEnclosingObjectStart(raw, keyIdx)

	return r
}

// checkObjectClosedRaw determines whether the enclosing JSON object closed
// after the "jobs" array by scanning the raw string from afterArrayIdx. If
// there is no unclosed '{' before the array (a bare array not wrapped in an
// object), the object is trivially closed. Otherwise, we scan for the
// enclosing '}', skipping JSON string values (a '}' inside a string is not
// the object close). A truncated summary value (cut mid-string) means the
// scan reaches end-of-string without finding '}' → objectClosed=false.
// Returns (closed, closeIdx) where closeIdx is the '}' offset, or -1 if not
// found or the array is bare.
func checkObjectClosedRaw(raw string, afterArrayIdx int) (bool, int) {
	if !hasUnclosedBraceBefore(raw, afterArrayIdx) {
		return true, -1 // bare array, no enclosing object to close
	}
	i := afterArrayIdx
	for i < len(raw) {
		switch raw[i] {
		case '"':
			// Skip a JSON string value (a '}' inside a string is not the
			// object close).
			i++
			for i < len(raw) {
				if raw[i] == '\\' && i+1 < len(raw) {
					i += 2
					continue
				}
				if raw[i] == '"' {
					i++
					break
				}
				i++
			}
		case '}':
			return true, i
		default:
			i++
		}
	}
	return false, -1
}

// hasUnclosedBraceBefore reports whether there is an unclosed '{' in raw
// before pos — i.e. the array at pos is inside an enclosing object. Uses a
// simple brace-depth counter (does not account for braces inside JSON string
// values; adequate for the practical inputs — model responses rarely contain
// literal braces in string fields, and the consequence of a false positive is
// a redundant decoder scan that still returns the correct answer).
func hasUnclosedBraceBefore(raw string, pos int) bool {
	depth := 0
	for i := 0; i < pos; i++ {
		switch raw[i] {
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		}
	}
	return depth > 0
}

// findEnclosingObjectStart scans forward from 0 to keyIdx (with brace-depth
// tracking and JSON string skipping) to find the '{' that opens the JSON
// object enclosing the "jobs" key. Returns the '{' offset, or -1 if the
// "jobs" key is not inside an enclosing object (a bare array).
func findEnclosingObjectStart(raw string, keyIdx int) int {
	// First pass: compute the brace depth at keyIdx.
	depth := 0
	inString := false
	for i := 0; i < keyIdx; i++ {
		if inString {
			if raw[i] == '\\' && i+1 < len(raw) {
				i++
				continue
			}
			if raw[i] == '"' {
				inString = false
			}
			continue
		}
		switch raw[i] {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		}
	}
	if depth == 0 {
		return -1 // no enclosing object
	}
	// Second pass: find the last '{' at the target depth that is still open
	// at keyIdx. A '{' at the target depth is the enclosing object's opening
	// brace; a '}' that drops depth below the target closes it, so we forget
	// it and keep scanning for the next one.
	targetDepth := depth
	depth = 0
	inString = false
	enclosingStart := -1
	for i := 0; i < keyIdx; i++ {
		if inString {
			if raw[i] == '\\' && i+1 < len(raw) {
				i++
				continue
			}
			if raw[i] == '"' {
				inString = false
			}
			continue
		}
		switch raw[i] {
		case '"':
			inString = true
		case '{':
			depth++
			if depth == targetDepth {
				enclosingStart = i
			}
		case '}':
			if depth > 0 {
				depth--
				if depth < targetDepth {
					enclosingStart = -1
				}
			}
		}
	}
	return enclosingStart
}

// extractSummaryFromBounds extracts the "summary" string field from within a
// bounded byte range [start, end) of the raw response — the enclosing object
// of the chosen jobs array. This binds the summary to the SAME object that
// produced the jobs, preventing a paraphrased echo summary from a different
// object (e.g. a format restatement whose abbreviated placeholder text does
// not match schemaPlaceholderSummary) from leaking in. The schema placeholder
// check remains as a belt: if the only summary in the bounded range is the
// placeholder, it is skipped and ("", false) is returned so the caller uses
// the synthetic fallback. Returns (value, terminated).
func extractSummaryFromBounds(raw string, start, end int) (string, bool) {
	if start < 0 || start >= end {
		return "", false
	}
	if end > len(raw) {
		end = len(raw)
	}
	segment := raw[start:end]
	val, terminated := extractSummaryField(segment)
	if terminated && val != schemaPlaceholderSummary {
		return val, true
	}
	return "", false
}

// extractSummaryField attempts to extract the "summary" string field from a
// possibly-truncated JSON response. Returns (value, terminated). terminated
// is false when the opening quote was found but no closing quote was reached
// before end-of-string — the value is a truncation fragment and is returned
// as "" so the caller falls back to the synthetic truncated-summary message
// instead of delivering a partial sentence mid-word.
//
// BLOCKER 1 fix — multi-occurrence scan: a model that restates the format
// before answering puts a placeholder "summary" (the prompt's example text)
// where the real summary should be. Anchoring on the FIRST "summary"
// occurrence returns the placeholder. We scan ALL occurrences, skip the
// schema placeholder, and return the first non-placeholder terminated
// summary. If only the placeholder is found, we return it as a last resort
// (terminated=true) — better than nothing. If nothing terminated, we return
// ("", false) so the caller uses the synthetic fallback.
//
// This is a fork of vendored llm.ExtractJSONAnswer (go-kit/llm), diverged to
// handle truncation (unterminated strings) and schema-placeholder skipping.
// Upstreaming the truncation-aware logic is the right long-term home; until
// then, this copy carries the two behaviours the vendored version lacks.
func extractSummaryField(raw string) (string, bool) {
	searchFrom := 0
	var firstTerminated string
	var foundTerminated bool
	for {
		idx := strings.Index(raw[searchFrom:], `"summary"`)
		if idx < 0 {
			break
		}
		idx += searchFrom
		val, terminated := extractSummaryAt(raw, idx)
		if terminated {
			if val != schemaPlaceholderSummary {
				return val, true
			}
			if !foundTerminated {
				firstTerminated = val
				foundTerminated = true
			}
		}
		searchFrom = idx + len(`"summary"`)
	}
	if foundTerminated {
		return firstTerminated, true
	}
	return "", false
}

// extractSummaryAt extracts the "summary" string value starting at idx (the
// position of `"summary"` in raw). Returns (value, terminated).
func extractSummaryAt(raw string, idx int) (string, bool) {
	rest := raw[idx+len(`"summary"`):]
	rest = strings.TrimSpace(rest)
	if len(rest) == 0 || rest[0] != ':' {
		return "", false
	}
	rest = strings.TrimSpace(rest[1:])
	if len(rest) == 0 || rest[0] != '"' {
		return "", false
	}
	rest = rest[1:]
	var sb strings.Builder
	for i := 0; i < len(rest); i++ {
		if rest[i] == '\\' && i+1 < len(rest) {
			switch rest[i+1] {
			case '"':
				sb.WriteByte('"')
				i++
				continue
			case '\\':
				sb.WriteByte('\\')
				i++
				continue
			case 'n':
				sb.WriteByte('\n')
				i++
				continue
			case 't':
				sb.WriteByte('\t')
				i++
				continue
			case 'r':
				sb.WriteByte('\r')
				i++
				continue
			case '/':
				sb.WriteByte('/')
				i++
				continue
			case 'b':
				sb.WriteByte('\b')
				i++
				continue
			case 'f':
				sb.WriteByte('\f')
				i++
				continue
			}
		}
		if rest[i] == '"' {
			return sb.String(), true
		}
		sb.WriteByte(rest[i])
	}
	return "", false
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
