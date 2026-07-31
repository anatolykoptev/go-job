package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/anatolykoptev/go-engine/llm"
	kitmetrics "github.com/anatolykoptev/go-kit/metrics"
)

// makeJobJSON builds a JSON response with n complete job records and an
// optional summary. Used by the falsification tests to craft canned LLM
// responses.
func makeJobJSON(n int, summary string) string {
	jobs := make([]JobListing, n)
	for i := range jobs {
		jobs[i] = JobListing{
			Title:       fmt.Sprintf("Software Engineer %d", i+1),
			Company:     fmt.Sprintf("Company %d", i+1),
			Location:    "Remote",
			Source:      "greenhouse",
			URL:         fmt.Sprintf("https://example.com/job/%d", i+1),
			JobType:     "full-time",
			Remote:      "remote",
			Experience:  "senior",
			Skills:      []string{"Go", "Kubernetes"},
			Description: "Build and operate distributed systems.",
			Posted:      "2 days ago",
		}
	}
	out := llmJobOutput{Jobs: jobs, Summary: summary}
	b, _ := json.Marshal(out)
	return string(b)
}

// truncateAt cuts the JSON string at the given position, simulating an LLM
// response truncated mid-token by the output token budget.
func truncateAt(s string, pos int) string {
	if pos >= len(s) {
		return s
	}
	return s[:pos]
}

// withTestRegistry swaps the package-level reg with a fresh in-memory
// registry (no prom bridge → no DefaultRegisterer collision) and pre-touches
// the extraction counters at 0 so rate()-floor assertions see a real 0→N
// transition. Returns the registry for snapshot assertions.
func withTestRegistry(t *testing.T) *kitmetrics.Registry {
	t.Helper()
	orig := reg
	t.Cleanup(func() { reg = orig })
	r := kitmetrics.NewRegistry()
	reg = r
	// Pre-touch all extraction outcomes at 0 — same pattern as
	// warmAlertBoundedMetrics. Without this, the first Incr would create
	// the series, and a "0 → 1" delta check would pass; but if the Incr
	// never fires (vacuous test), the series would be absent and the
	// snapshot would return 0 — indistinguishable from "fired and landed".
	// Pre-touching makes the baseline explicit and the delta check meaningful.
	for _, oc := range []string{ExtractionOK, ExtractionTrailingGarbage, ExtractionTruncatedSalvaged, ExtractionUnparseable} {
		r.Add(MetricJobSearchExtraction+"{outcome="+oc+"}", 0)
	}
	return r
}

// extractionDelta returns the current value of the extraction counter for the
// given outcome from a registry snapshot.
func extractionDelta(t *testing.T, r *kitmetrics.Registry, outcome string) int64 {
	t.Helper()
	snap := r.Snapshot()
	key := MetricJobSearchExtraction + "{outcome=" + outcome + "}"
	v, ok := snap[key]
	if !ok {
		t.Fatalf("extraction counter %q absent from snapshot — pre-touch failed", key)
	}
	return v
}

// withJobSearchComplete swaps the LLM completion seam with a canned function
// that returns rawResp. The seam is the ONLY way to drive SummarizeJobResults
// without a real LLM call — the test injects the exact truncated response the
// production bug produced.
func withJobSearchComplete(t *testing.T, rawResp string) {
	t.Helper()
	orig := jobSearchComplete
	t.Cleanup(func() { jobSearchComplete = orig })
	jobSearchComplete = func(_ context.Context, _ string, _ float64, _ int, _ ...llm.ChatOption) (string, error) {
		return rawResp, nil
	}
}

// --- F1: truncated response must never become a silent empty result ---

// TestSummarizeJobResults_TruncatedResponse_NeverSilent drives the FULL
// SummarizeJobResults path (via the jobSearchComplete seam) with a canned
// LLM response truncated mid-record — exactly the production failure. This
// is the test the PR exists to gate: it asserts (a) Jobs is NOT nil, (b) the
// truncated_salvaged counter moved, (c) the raw JSON is not in Summary.
//
// Mutation: wholesale revert of SummarizeJobResults to its pre-PR body
// (SummarizeToJSON + "if parsed == nil { return &JobSearchOutput{Query: query,
// Summary: raw}, nil }") → this test goes RED because Jobs would be nil and
// the outcome counter would never fire (the old path never calls
// parseJobSearchResponse or IncrJobSearchExtraction).
func TestSummarizeJobResults_TruncatedResponse_NeverSilent(t *testing.T) {
	r := withTestRegistry(t)

	// Build a 10-record response, then truncate mid-record-9 so 8 complete
	// records are salvageable.
	full := makeJobJSON(10, "Good matches found.")
	marker := `"title":"Software Engineer 9"`
	cutIdx := strings.Index(full, marker)
	if cutIdx < 0 {
		t.Fatalf("test setup: marker %q not found in JSON", marker)
	}
	truncated := truncateAt(full, cutIdx+len(marker)-3)

	withJobSearchComplete(t, truncated)

	out, err := SummarizeJobResults(context.Background(), "golang engineer", JobSearchInstruction, 5000, nil, nil)
	if err != nil {
		t.Fatalf("SummarizeJobResults error: %v", err)
	}

	// (a) Jobs is NOT nil — the silent-empty-result bug is absent.
	if out.Jobs == nil {
		t.Fatal("Jobs is nil — the silent-empty-result bug is present")
	}
	if len(out.Jobs) < 8 {
		t.Errorf("salvaged %d jobs, want ≥ 8 complete records", len(out.Jobs))
	}

	// (b) The truncated_salvaged counter moved.
	got := extractionDelta(t, r, ExtractionTruncatedSalvaged)
	if got != 1 {
		t.Errorf("truncated_salvaged counter = %d, want 1 (delta from baseline 0)", got)
	}

	// (c) The raw JSON is NOT in Summary (the old bug stuffed it there).
	if strings.Contains(out.Summary, `"title":`) {
		t.Error("Summary contains raw JSON — the stuff-raw-into-summary bug is present")
	}
}

// TestParseJobSearchResponse_TruncatedSalvagesCompleteRecords feeds a canned
// LLM response cut mid-record to the pure parser and asserts the salvage
// mechanics: complete records returned, outcome=truncated_salvaged, dropped≥1.
//
// This tests the parser in isolation; the SummarizeJobResults-level gate is
// F1 above. No mutation claim here — the parser is a pure function whose
// behaviour is directly asserted.
func TestParseJobSearchResponse_TruncatedSalvagesCompleteRecords(t *testing.T) {
	full := makeJobJSON(10, "Good matches found.")
	marker := `"title":"Software Engineer 9"`
	cutIdx := strings.Index(full, marker)
	if cutIdx < 0 {
		t.Fatalf("test setup: marker %q not found in JSON", marker)
	}
	truncated := truncateAt(full, cutIdx+len(marker)-3)

	jobs, _, outcome, salvaged, dropped := parseJobSearchResponse(truncated)

	if len(jobs) < 8 {
		t.Errorf("salvaged %d jobs, want ≥ 8 complete records", len(jobs))
	}
	if outcome != ExtractionTruncatedSalvaged {
		t.Errorf("outcome = %q, want %q", outcome, ExtractionTruncatedSalvaged)
	}
	if jobs == nil {
		t.Fatal("jobs is nil — the silent-empty-result bug is present")
	}
	if salvaged != len(jobs) {
		t.Errorf("salvaged = %d, len(jobs) = %d — must match", salvaged, len(jobs))
	}
	if dropped < 1 {
		t.Errorf("dropped = %d, want ≥ 1 (truncated tail)", dropped)
	}
}

// TestIncrJobSearchExtraction_TruncatedCounterMoves verifies the
// job_search_extraction_total{outcome=truncated_salvaged} counter actually
// lands in the registry when IncrJobSearchExtraction is called with the
// truncated_salvaged outcome. Guards against vacuity: the registry is
// pre-touched at 0, the baseline is asserted, and the post-Incr delta is
// asserted to be exactly 1.
//
// Mutation: restore the silent fallback (never call IncrJobSearchExtraction)
// → the delta stays 0 and this test goes RED.
func TestIncrJobSearchExtraction_TruncatedCounterMoves(t *testing.T) {
	r := withTestRegistry(t)

	base := extractionDelta(t, r, ExtractionTruncatedSalvaged)
	if base != 0 {
		t.Fatalf("baseline delta = %d, want 0 — pre-touch invariant broken", base)
	}

	IncrJobSearchExtraction(ExtractionTruncatedSalvaged)

	got := extractionDelta(t, r, ExtractionTruncatedSalvaged)
	if got != 1 {
		t.Errorf("after Incr, counter = %d, want 1 (delta from baseline 0)", got)
	}
}

// --- F2: full-limit extraction completes with no truncation outcome ---

// TestParseJobSearchResponse_FullLimit50Completes feeds a complete 50-record
// JSON response (the documented max limit) and asserts the outcome is ok with
// no truncation.
func TestParseJobSearchResponse_FullLimit50Completes(t *testing.T) {
	full := makeJobJSON(50, "50 matching positions found.")
	jobs, _, outcome, _, dropped := parseJobSearchResponse(full)

	if len(jobs) != 50 {
		t.Errorf("parsed %d jobs, want 50", len(jobs))
	}
	if outcome != ExtractionOK {
		t.Errorf("outcome = %q, want %q (full parse must succeed)", outcome, ExtractionOK)
	}
	if dropped != 0 {
		t.Errorf("dropped = %d, want 0 for a complete response", dropped)
	}
}

// --- F4: instruction constant carries no rejection directive ---

// TestJobSearchInstruction_NoRejectionDirective asserts the #404
// relevance-rejection rule ("return ONLY jobs relevant to the query
// keywords") has been removed from JobSearchInstruction. The model's job on
// this path is to EXTRACT structured fields, not to select which listings are
// relevant.
//
// Mutation: re-add the #404 sentence → this test goes RED.
func TestJobSearchInstruction_NoRejectionDirective(t *testing.T) {
	banned := []string{
		"return ONLY jobs relevant",
		"return ONLY jobs relevant to the query keywords",
		"match against title, company, skills, description",
	}
	for _, phrase := range banned {
		if strings.Contains(JobSearchInstruction, phrase) {
			t.Errorf("JobSearchInstruction still contains rejection directive %q — must be removed (relevance is the ranking stage's job)", phrase)
		}
	}

	// The honest-empty summary branch must remain (when the pipeline
	// genuinely hands over zero listings, the summary should say so).
	if !strings.Contains(JobSearchInstruction, "honest statement that none match") {
		t.Error("JobSearchInstruction lost the honest-empty summary directive — only the rejection rule should be removed")
	}
}

// TestJobSearchInstruction_LimitCapMatchesConfig asserts the prompt's
// extraction cap matches JobSearchInput.Limit's documented max (50), not 15.
// An operator setting limit=40 must not silently get ≤15.
//
// Mutation: revert the prompt to "up to 15" → this test goes RED.
func TestJobSearchInstruction_LimitCapMatchesConfig(t *testing.T) {
	if strings.Contains(JobSearchInstruction, "up to 15") {
		t.Error("JobSearchInstruction still says 'up to 15' — limits 16-50 are dead configuration")
	}
	if !strings.Contains(JobSearchInstruction, "up to 50") {
		t.Error("JobSearchInstruction must say 'up to 50' to match JobSearchInput.Limit max")
	}
}

// --- F5: unparseable response is counted, not silently swallowed ---

// TestParseJobSearchResponse_UnparseableResponse verifies that a completely
// garbled response (no extractable records) is classified as unparseable with
// a counter, not silently turned into an empty result with raw JSON in summary.
func TestParseJobSearchResponse_UnparseableResponse(t *testing.T) {
	garbled := `{"jobs": [this is not valid json at all`
	jobs, summary, outcome, salvaged, dropped := parseJobSearchResponse(garbled)

	if len(jobs) > 0 {
		t.Errorf("unparseable response returned %d jobs, want 0", len(jobs))
	}
	if outcome != ExtractionUnparseable {
		t.Errorf("outcome = %q, want %q", outcome, ExtractionUnparseable)
	}
	if salvaged != 0 || dropped != 0 {
		t.Errorf("salvaged=%d dropped=%d, want 0/0 for unparseable", salvaged, dropped)
	}
	if strings.Contains(summary, "this is not valid json") {
		t.Error("summary contains raw garbled text — the stuff-raw-into-summary bug")
	}
}

// TestIncrJobSearchExtraction_OKCounterMoves verifies the ok outcome counter
// lands in the registry. Companion to the truncated_salvaged counter test.
func TestIncrJobSearchExtraction_OKCounterMoves(t *testing.T) {
	r := withTestRegistry(t)
	base := extractionDelta(t, r, ExtractionOK)
	if base != 0 {
		t.Fatalf("baseline = %d, want 0", base)
	}
	IncrJobSearchExtraction(ExtractionOK)
	got := extractionDelta(t, r, ExtractionOK)
	if got != 1 {
		t.Errorf("after Incr, counter = %d, want 1", got)
	}
}

// TestIncrJobSearchExtraction_UnrecognisedDropped verifies an unrecognised
// outcome is silently dropped (cardinality guard — no free-form strings).
func TestIncrJobSearchExtraction_UnrecognisedDropped(t *testing.T) {
	r := withTestRegistry(t)
	IncrJobSearchExtraction("definitely-not-a-valid-outcome")
	snap := r.Snapshot()
	for k := range snap {
		if strings.Contains(k, "definitely-not-a-valid-outcome") {
			t.Errorf("unrecognised outcome leaked into registry: %s", k)
		}
	}
}

// --- MAJOR 1: a complete response with trailing text is NOT truncated ---

// TestParseJobSearchResponse_TrailingTextNotTruncated feeds a complete 3-record
// JSON response followed by trailing prose ("Hope this helps!") — the shape a
// chat-tuned model produces. json.Unmarshal rejects trailing non-whitespace,
// so the salvage path runs, but the array closed cleanly (io.EOF). The
// outcome must be trailing_garbage, NOT truncated_salvaged, and dropped must
// be 0. A rate() alert on truncated_salvaged must not fire on healthy traffic.
func TestParseJobSearchResponse_TrailingTextNotTruncated(t *testing.T) {
	raw := makeJobJSON(3, "Three good matches.") + "\n\nHope this helps!"
	jobs, _, outcome, _, dropped := parseJobSearchResponse(raw)

	if len(jobs) != 3 {
		t.Errorf("got %d jobs, want 3 (all records kept)", len(jobs))
	}
	if outcome != ExtractionTrailingGarbage {
		t.Errorf("outcome = %q, want %q (array closed cleanly — trailing text is not truncation)", outcome, ExtractionTrailingGarbage)
	}
	if dropped != 0 {
		t.Errorf("dropped = %d, want 0 (nothing was lost)", dropped)
	}
}

// --- MAJOR 2: a valid EMPTY result with trailing text preserves the honest summary ---

// TestParseJobSearchResponse_EmptyArrayTrailingText_PreservesSummary feeds a
// legitimately empty jobs array with the model's honest "none match" summary,
// followed by trailing text. The salvage path must recognise the array closed
// cleanly (count=0, arrayClosed=true) and preserve the summary — NOT replace
// it with a parse-error message. This is the class the PR closes: a
// successful-looking result carrying the wrong content.
func TestParseJobSearchResponse_EmptyArrayTrailingText_PreservesSummary(t *testing.T) {
	raw := `{"jobs":[],"summary":"None of the listings match your query."}` + "\ntrailing"
	jobs, summary, outcome, _, dropped := parseJobSearchResponse(raw)

	if len(jobs) != 0 {
		t.Errorf("got %d jobs, want 0 (legitimate empty array)", len(jobs))
	}
	if outcome != ExtractionTrailingGarbage {
		t.Errorf("outcome = %q, want %q (empty array closed cleanly)", outcome, ExtractionTrailingGarbage)
	}
	if dropped != 0 {
		t.Errorf("dropped = %d, want 0", dropped)
	}
	if !strings.Contains(summary, "None of the listings match") {
		t.Errorf("summary = %q — the model's honest explanation was replaced by a parse-error message", summary)
	}
}

// --- MAJOR 5: a truncated summary is not delivered verbatim unmarked ---

// TestParseJobSearchResponse_TruncatedSummaryNotDeliveredVerbatim feeds a
// response where the jobs array is complete but the summary field is cut
// mid-string (no closing quote). extractSummaryField must signal
// "unterminated" and return "" so the salvage path does not deliver a partial
// sentence that stops mid-word. The array closed cleanly, so the outcome is
// trailing_garbage (not truncated_salvaged — no records were lost); the key
// assertion is that the partial summary value is absent from the output.
func TestParseJobSearchResponse_TruncatedSummaryNotDeliveredVerbatim(t *testing.T) {
	// Complete 2-record array, then a summary field cut mid-value.
	raw := `{"jobs":[` +
		`{"title":"Eng 1","company":"Co","location":"Remote","source":"greenhouse","url":"https://ex.com/1","job_type":"full-time","remote":"remote","experience":"senior","skills":["Go"],"description":"Build things.","posted":"2d ago"},` +
		`{"title":"Eng 2","company":"Co","location":"Remote","source":"greenhouse","url":"https://ex.com/2","job_type":"full-time","remote":"remote","experience":"senior","skills":["Go"],"description":"Build things.","posted":"2d ago"}],` +
		`"summary":"These two roles look strongest because they ma`

	jobs, summary, outcome, _, _ := parseJobSearchResponse(raw)

	if len(jobs) != 2 {
		t.Errorf("got %d jobs, want 2 (array complete)", len(jobs))
	}
	// Array closed cleanly → trailing_garbage (no records lost).
	if outcome != ExtractionTrailingGarbage {
		t.Errorf("outcome = %q, want %q (array closed, summary truncated)", outcome, ExtractionTrailingGarbage)
	}
	// The partial summary must NOT be delivered verbatim.
	if strings.Contains(summary, "These two roles look strongest because they ma") {
		t.Error("summary contains the partial unterminated value — a mid-word sentence was delivered unmarked")
	}
}

// TestExtractSummaryField_UnterminatedReturnsEmpty verifies the
// extractSummaryField helper signals unterminated and returns "" when the
// closing quote is missing.
func TestExtractSummaryField_UnterminatedReturnsEmpty(t *testing.T) {
	raw := `"summary":"partial sentence with no end`
	val, terminated := extractSummaryField(raw)
	if terminated {
		t.Error("terminated = true, want false (no closing quote)")
	}
	if val != "" {
		t.Errorf("value = %q, want \"\" (unterminated summary must be dropped)", val)
	}
}

// TestExtractSummaryField_EscapeHandling verifies the escape sequences that
// the original fork of llm.ExtractJSONAnswer mishandled (\t, \r, \/, \b, \f
// passed through literally). Each must decode to its real character.
func TestExtractSummaryField_EscapeHandling(t *testing.T) {
	cases := []struct {
		raw   string
		want  string
		label string
	}{
		{`"summary":"a\tb"`, "a\tb", `tab`},
		{`"summary":"a\rb"`, "a\rb", `carriage return`},
		{`"summary":"a\/b"`, "a/b", `forward slash`},
		{`"summary":"a\bb"`, "a\bb", `backspace`},
		{`"summary":"a\fb"`, "a\fb", `form feed`},
		{`"summary":"a\"b"`, `a"b`, `double quote`},
		{`"summary":"a\nb"`, "a\nb", `newline`},
		{`"summary":"a\\b"`, `a\b`, `backslash`},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			got, ok := extractSummaryField(c.raw)
			if !ok {
				t.Fatalf("terminated = false, want true")
			}
			if got != c.want {
				t.Errorf("escape %s: got %q, want %q", c.label, got, c.want)
			}
		})
	}
}
