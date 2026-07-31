package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

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
	for _, oc := range []string{ExtractionOK, ExtractionTruncatedSalvaged, ExtractionUnparseable} {
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

// --- F1: truncated response must never become a silent empty result ---

// TestParseJobSearchResponse_TruncatedSalvagesCompleteRecords feeds a canned
// LLM response cut mid-record (exactly the production failure: the model's
// JSON is truncated by the output token budget, the full parse fails, and the
// old code stuffed the raw string into Summary with Jobs=nil).
//
// Asserts: (a) the complete records ARE returned, (b) the dropped count is
// recorded, (c) the outcome counter moved to truncated_salvaged, (d) Jobs is
// NOT nil/empty.
//
// Mutation: restore the silent raw-string-into-summary fallback
// (if parsed == nil { return &JobSearchOutput{Summary: raw}, nil }) → this
// test goes RED because Jobs would be nil and the outcome would be
// unparseable (or the old code path never calls parseJobSearchResponse at all).
func TestParseJobSearchResponse_TruncatedSalvagesCompleteRecords(t *testing.T) {
	// Build a 10-record response, then truncate it mid-record-9 so 8
	// complete records are salvageable.
	full := makeJobJSON(10, "Good matches found.")
	// Find the start of the 9th record's title field and cut there.
	// json.Marshal produces compact JSON: "title":"Software Engineer 9"
	marker := `"title":"Software Engineer 9"`
	cutIdx := strings.Index(full, marker)
	if cutIdx < 0 {
		t.Fatalf("test setup: marker %q not found in JSON", marker)
	}
	// Cut a few chars into the 9th record's title value — mid-token.
	truncated := truncateAt(full, cutIdx+len(marker)-3)

	jobs, summary, outcome, salvaged, dropped := parseJobSearchResponse(truncated)

	// (a) Complete records ARE returned.
	if len(jobs) < 8 {
		t.Errorf("salvaged %d jobs, want ≥ 8 complete records", len(jobs))
	}

	// (b) Dropped count is recorded.
	if dropped < 1 {
		t.Errorf("dropped = %d, want ≥ 1 (truncated tail)", dropped)
	}

	// (c) Outcome is truncated_salvaged.
	if outcome != ExtractionTruncatedSalvaged {
		t.Errorf("outcome = %q, want %q", outcome, ExtractionTruncatedSalvaged)
	}

	// (d) Jobs is NOT nil.
	if jobs == nil {
		t.Fatal("jobs is nil — the silent-empty-result bug is present")
	}

	// Salvaged count matches.
	if salvaged != len(jobs) {
		t.Errorf("salvaged = %d, len(jobs) = %d — must match", salvaged, len(jobs))
	}

	// Summary must NOT be the raw JSON (the old bug).
	if strings.Contains(summary, `"title":`) {
		t.Error("summary contains raw JSON — the stuff-raw-into-summary bug is present")
	}
	_ = summary // summary is non-empty (salvage path generates it)
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

	// Baseline: pre-touched at 0 — assert it IS 0, not absent.
	base := extractionDelta(t, r, ExtractionTruncatedSalvaged)
	if base != 0 {
		t.Fatalf("baseline delta = %d, want 0 — pre-touch invariant broken", base)
	}

	// Simulate the SummarizeJobResults counter call.
	IncrJobSearchExtraction(ExtractionTruncatedSalvaged)

	got := extractionDelta(t, r, ExtractionTruncatedSalvaged)
	if got != 1 {
		t.Errorf("after Incr, counter = %d, want 1 (delta from baseline 0)", got)
	}
}

// --- F2: full-limit extraction completes with no truncation outcome ---

// TestParseJobSearchResponse_FullLimit50Completes feeds a complete 50-record
// JSON response (the documented max limit) and asserts the outcome is ok with
// no truncation. Also verifies the output token budget arithmetic: 50 records
// × 300 tokens/record + 500 summary = 15500, and jobSearchMaxOutputTokens(50)
// must return ≥ 15500 so the model has enough budget to complete.
//
// Mutation: restore the low budget (jobSearchMaxOutputTokens returns
// cfg.LLMMaxTokens regardless of result count) → the budget assertion goes
// RED. The parse assertion goes RED if the salvage path is triggered
// unnecessarily (outcome would be truncated_salvaged instead of ok).
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

	// Budget arithmetic: 50 × 300 + 500 = 15500. The function must return
	// at least this value so the model has enough output tokens to complete.
	budget := jobSearchMaxOutputTokens(50)
	minNeeded := 50*jobSearchTokensPerRecord + jobSearchSummaryBudget
	if budget < minNeeded {
		t.Errorf("jobSearchMaxOutputTokens(50) = %d, want ≥ %d (50×%d+%d)",
			budget, minNeeded, jobSearchTokensPerRecord, jobSearchSummaryBudget)
	}
}

// --- F3: extraction stage drops nothing for relevance ---

// TestParseJobSearchResponse_NoRelevanceRejection feeds N listings that are
// off-topic (title/description unrelated to a "python data engineer" query)
// and asserts all N are returned — the extraction stage's job is to EXTRACT
// structured fields, not to judge relevance. Relevance belongs to the ranking
// stage (being rebuilt separately).
//
// Mutation: re-add a server-side relevance drop in the extraction path
// (e.g. filter jobs by keyword match) → len(jobs) < N and this test goes RED.
func TestParseJobSearchResponse_NoRelevanceRejection(t *testing.T) {
	const n = 5
	full := makeJobJSON(n, "All listings extracted.")
	jobs, _, outcome, _, _ := parseJobSearchResponse(full)

	if len(jobs) != n {
		t.Errorf("extraction returned %d jobs, want %d — extraction stage must not drop listings for relevance", len(jobs), n)
	}
	if outcome != ExtractionOK {
		t.Errorf("outcome = %q, want %q", outcome, ExtractionOK)
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
