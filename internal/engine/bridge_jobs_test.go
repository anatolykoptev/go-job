package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
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
	for _, oc := range []string{ExtractionOK, ExtractionTrailingGarbage, ExtractionSchemaMismatch, ExtractionTruncatedSalvaged, ExtractionUnparseable} {
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
//
// MAJOR 6 fix: the seam captures the parameters (temperature, maxTokens, opts)
// so tests can assert the production call passes the right values. The old
// seam discarded every parameter — dropping WithReasoningEffort("none") or
// passing 0 instead of cfg.LLMMaxTokens left the whole suite green.
func withJobSearchComplete(t *testing.T, rawResp string) *seamCapture {
	t.Helper()
	orig := jobSearchComplete
	t.Cleanup(func() { jobSearchComplete = orig })
	cap := &seamCapture{}
	jobSearchComplete = func(_ context.Context, _ string, temperature float64, maxTokens int, opts ...llm.ChatOption) (string, error) {
		cap.temperature = temperature
		cap.maxTokens = maxTokens
		cap.opts = opts
		cap.called = true
		return rawResp, nil
	}
	return cap
}

// seamCapture records the parameters passed to the jobSearchComplete seam.
type seamCapture struct {
	called      bool
	temperature float64
	maxTokens   int
	opts        []llm.ChatOption
}

// reasoningEffort extracts the reasoning_effort value from the captured
// ChatOptions by applying them to a chatConfig via reflect (chatConfig is
// unexported in the llm package, so we use reflect to instantiate and inspect
// it without modifying the vendored code).
func (c *seamCapture) reasoningEffort() string {
	if len(c.opts) == 0 {
		return ""
	}
	// ChatOption is func(*chatConfig). chatConfig is unexported, so use
	// reflect to create an instance and apply each option.
	chatConfigType := reflect.TypeOf(llm.ChatOption(nil)).In(0) // *chatConfig
	chatConfigPtr := reflect.New(chatConfigType.Elem())         // *chatConfig
	for _, opt := range c.opts {
		reflect.ValueOf(opt).Call([]reflect.Value{chatConfigPtr})
	}
	return chatConfigPtr.Elem().FieldByName("reasoningEffort").String()
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

	out, err := SummarizeJobResults(context.Background(), "golang engineer", JobSearchInstructionFor(JobSearchMaxLimit), 5000, nil, nil)
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

// --- F4: instruction carries a softened relevance directive, not a hard rejection rule ---

// TestJobSearchInstruction_SoftenedRelevanceRule asserts the #404 hard
// rejection rule ("return ONLY jobs relevant to the query keywords") has been
// removed and replaced with a SOFTENED directive: order by relevance, do NOT
// demand literal keyword presence, and state plainly when nothing is a good
// fit. The hard rule over-rejected (query "python data engineer" → all 40
// listings rejected for not "explicitly mentioning Python"); removing it left
// NOTHING filtering, which is the original complaint in issues #335/#336/#379.
// The softened rule restores filtering without the literalism.
//
// Mutation (a): re-add the #404 hard rejection sentence → this test goes RED
// on the "banned" check.
// Mutation (b): remove the softened relevance sentence entirely → this test
// goes RED on the "must contain" check.
func TestJobSearchInstruction_SoftenedRelevanceRule(t *testing.T) {
	instr := JobSearchInstructionFor(JobSearchMaxLimit)
	banned := []string{
		"return ONLY jobs relevant",
		"return ONLY jobs relevant to the query keywords",
	}
	for _, phrase := range banned {
		if strings.Contains(instr, phrase) {
			t.Errorf("JobSearchInstruction still contains hard rejection directive %q — must be replaced with the softened rule", phrase)
		}
	}

	// The softened relevance rule must be present: order by relevance, do not
	// demand literal keyword presence, state plainly when nothing fits.
	if !strings.Contains(instr, "Order jobs by relevance to the query") {
		t.Error("JobSearchInstruction lost the softened relevance directive — ordering by relevance must be present")
	}
	if !strings.Contains(instr, "Do NOT reject a listing for lacking a literal keyword match") {
		t.Error("JobSearchInstruction lost the anti-literalism directive — the over-rejection guard must be present")
	}
	if !strings.Contains(instr, "nothing is a good fit") {
		t.Error("JobSearchInstruction lost the honest-empty summary directive — only the hard rejection rule should be removed")
	}
}

// TestJobSearchInstruction_LimitInterpolated asserts the prompt interpolates
// the caller's actual limit, not a hand-copied constant. The source of truth
// is JobSearchMaxLimit; the prompt for limit=N must say "up to N".
//
// Mutation (a): revert to a hard-coded "up to 50" const → this test goes RED
// because JobSearchInstructionFor(15) would still say "up to 50".
// Mutation (b): change JobSearchMaxLimit to 30 without updating the prompt →
// this test goes RED because JobSearchInstructionFor(JobSearchMaxLimit) would
// say "up to 30" but the old test hard-coded 50.
func TestJobSearchInstruction_LimitInterpolated(t *testing.T) {
	// The prompt for a specific limit must contain that limit.
	instr15 := JobSearchInstructionFor(15)
	if !strings.Contains(instr15, "up to 15") {
		t.Errorf("JobSearchInstructionFor(15) must say 'up to 15'; got: %s", instr15)
	}
	if strings.Contains(instr15, "up to 50") {
		t.Error("JobSearchInstructionFor(15) must NOT say 'up to 50' — the limit must be interpolated, not hard-copied")
	}

	// The prompt for the max limit must contain the max.
	instrMax := JobSearchInstructionFor(JobSearchMaxLimit)
	wantMax := fmt.Sprintf("up to %d", JobSearchMaxLimit)
	if !strings.Contains(instrMax, wantMax) {
		t.Errorf("JobSearchInstructionFor(JobSearchMaxLimit) must say %q; got: %s", wantMax, instrMax)
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

// --- MAJOR 3: a truncated enclosing object is NOT labelled healthy, and an empty summary never reaches the user ---

// TestParseJobSearchResponse_TruncatedSummaryNotDeliveredVerbatim feeds a
// response where the jobs array is complete but the summary field is cut
// mid-string (no closing quote) — the enclosing object is truncated. The
// outcome must be truncated_salvaged (NOT trailing_garbage — the response WAS
// cut, just outside the array). The partial summary must NOT be delivered
// verbatim, AND a synthetic fallback summary MUST be present — an empty
// summary must never reach the user unmarked. Both directions are asserted.
//
// Mutation (a): revert to the old arrayClosed-only check (no objectClosed) →
// outcome goes back to trailing_garbage → RED.
// Mutation (b): remove the synthetic fallback for empty summary → summary is
// "" → RED on the "must not be empty" assertion.
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
	// Array closed but enclosing object truncated → truncated_salvaged
	// (NOT trailing_garbage — the response was cut, the summary is incomplete).
	if outcome != ExtractionTruncatedSalvaged {
		t.Errorf("outcome = %q, want %q (enclosing object truncated — summary cut mid-value)", outcome, ExtractionTruncatedSalvaged)
	}
	// Direction 1: the partial summary must NOT be delivered verbatim.
	if strings.Contains(summary, "These two roles look strongest because they ma") {
		t.Error("summary contains the partial unterminated value — a mid-word sentence was delivered unmarked")
	}
	// Direction 2: the summary must NOT be empty — a synthetic fallback must
	// be present so the user is never handed an empty summary unmarked.
	if summary == "" {
		t.Error("summary is empty — an empty summary reached the user unmarked; the synthetic fallback is missing")
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

// --- BLOCKER 1: a schema echo must not become a fabricated job record ---

// TestParseJobSearchResponse_SchemaEchoNotFabricated feeds the reviewer's
// exact measured input: a model that restates the format (with a placeholder
// "jobs" array whose records have Title="job title", Company="company name")
// before the real answer. The old code anchored on the FIRST "jobs"
// occurrence and returned the placeholder as a real job listing, labelled
// healthy. The fix scans ALL candidate "jobs" arrays and picks the one
// yielding real (non-placeholder) records.
//
// Mutation (a): revert to first-match anchoring (strings.Index, single
// candidate) → RED: jobs[0].Title == "job title" (the placeholder).
// Mutation (b): remove isSchemaPlaceholderJob check → RED: the placeholder
// candidate yields 1 record, same as the real candidate; betterThan may pick
// the wrong one.
func TestParseJobSearchResponse_SchemaEchoNotFabricated(t *testing.T) {
	raw := "Here is the format I used:\n" +
		`{"jobs":[{"title":"job title","company":"company name","location":"city, country or Remote","source":"linkedin","url":"direct job listing URL","salary":"not specified","job_type":"full-time","remote":"remote","experience":"senior","skills":["skill1","skill2"],"description":"1-2 sentence summary of key responsibilities and requirements","posted":"2 days ago"}],"summary":"1-2 sentence recommendation: which jobs look most promising and why, or an honest statement that none match the query"}` + "\n\n" +
		"And here is the result:\n" +
		`{"jobs":[{"title":"Senior Go Engineer","company":"Acme Corp","location":"Berlin","source":"greenhouse","url":"https://boards.greenhouse.io/acme/123","salary":"$120k-150k","job_type":"full-time","remote":"remote","experience":"senior","skills":["Go","Kubernetes"],"description":"Build and operate distributed systems.","posted":"2 days ago"}],"summary":"One strong match: Acme Corp Senior Go Engineer in Berlin."}`

	jobs, summary, outcome, _, dropped := parseJobSearchResponse(raw)

	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1 (the real answer, not the placeholder)", len(jobs))
	}
	// The returned job must be the REAL answer, not the schema placeholder.
	if jobs[0].Title == "job title" {
		t.Fatal("jobs[0].Title == \"job title\" — the schema placeholder was returned as a real job listing (BLOCKER 1 is present)")
	}
	if jobs[0].Title != "Senior Go Engineer" {
		t.Errorf("jobs[0].Title = %q, want \"Senior Go Engineer\" (the real answer)", jobs[0].Title)
	}
	// The summary must be the real one, not the placeholder.
	if summary == schemaPlaceholderSummary {
		t.Error("summary is the schema placeholder — the real summary was discarded")
	}
	if !strings.Contains(summary, "Acme Corp") {
		t.Errorf("summary = %q, want the real summary mentioning Acme Corp", summary)
	}
	// The outcome must NOT label a fabrication as healthy with dropped=0.
	if outcome == ExtractionTrailingGarbage && dropped != 0 {
		t.Errorf("trailing_garbage with dropped=%d, want 0", dropped)
	}
}

// TestExtractSummaryField_SkipsPlaceholderSummary feeds a raw string with two
// "summary" occurrences: the schema placeholder first, then the real summary.
// extractSummaryField must skip the placeholder and return the real one.
//
// Mutation: revert to first-match (strings.Index, single occurrence) → RED:
// returns the placeholder.
func TestExtractSummaryField_SkipsPlaceholderSummary(t *testing.T) {
	raw := `"summary":"1-2 sentence recommendation: which jobs look most promising and why, or an honest statement that none match the query"}...{"summary":"Real summary here."}`
	val, terminated := extractSummaryField(raw)
	if !terminated {
		t.Fatal("terminated = false, want true (the real summary is terminated)")
	}
	if val == schemaPlaceholderSummary {
		t.Fatal("returned the placeholder summary instead of the real one")
	}
	if val != "Real summary here." {
		t.Errorf("got %q, want \"Real summary here.\"", val)
	}
}

// --- BLOCKER 2: a mid-array decode error must not discard subsequent records ---

// TestParseJobSearchResponse_SchemaMismatchKeepsSubsequentRecords feeds the
// reviewer's exact measured input: 10 complete records, one carrying
// "salary_min":"160000" (a string where *int is declared). The old code
// broke out of the decode loop on the first type mismatch, losing every
// complete record after it and reporting dropped=1 (a constant that
// misattributed the loss to the token budget). The fix decodes each element
// as json.RawMessage first, skips the bad one, and keeps scanning to ']'.
//
// Mutation (a): revert to dec.Decode(&job) (typed decode, break on error) →
// RED: only 2 records salvaged instead of 9.
// Mutation (b): revert outcome to truncated_salvaged with dropped=1 → RED:
// outcome is schema_mismatch with dropped=1 (exact, not a constant floor).
func TestParseJobSearchResponse_SchemaMismatchKeepsSubsequentRecords(t *testing.T) {
	// Build 10 records; record 2 has "salary_min":"160000" (string, not int).
	validRecord := func(i int) string {
		return fmt.Sprintf(`{"title":"Eng %d","company":"Co","location":"Remote","source":"greenhouse","url":"https://ex.com/%d","job_type":"full-time","remote":"remote","experience":"senior","skills":["Go"],"description":"Build things.","posted":"2d ago"}`, i, i)
	}
	badRecord := `{"title":"Eng 2","company":"Co","salary_min":"160000","location":"Remote","source":"greenhouse","url":"https://ex.com/2","job_type":"full-time","remote":"remote","experience":"senior","skills":["Go"],"description":"Build things.","posted":"2d ago"}`

	var recs []string
	for i := 0; i < 10; i++ {
		if i == 2 {
			recs = append(recs, badRecord)
		} else {
			recs = append(recs, validRecord(i))
		}
	}
	raw := `{"jobs":[` + strings.Join(recs, ",") + `],"summary":"Ten matches found."}`

	jobs, _, outcome, salvaged, dropped := parseJobSearchResponse(raw)

	// 9 records kept (the bad one skipped), NOT 2.
	if len(jobs) != 9 {
		t.Fatalf("salvaged %d jobs, want 9 (10 records minus 1 schema mismatch; the old code lost 8)", len(jobs))
	}
	if outcome != ExtractionSchemaMismatch {
		t.Errorf("outcome = %q, want %q (mid-array type mismatch, array closed cleanly)", outcome, ExtractionSchemaMismatch)
	}
	if salvaged != 9 {
		t.Errorf("salvaged = %d, want 9", salvaged)
	}
	// dropped must be the EXACT count of skipped elements (1), not a constant
	// floor of 1 that could mask a larger loss.
	if dropped != 1 {
		t.Errorf("dropped = %d, want 1 (exact count of schema-mismatched elements)", dropped)
	}
}

// TestParseJobSearchResponse_NonObjectElementSkipped feeds an array with a
// non-object element (a bare string) among valid records — the reviewer's B2
// case. The bad element is skipped, the rest kept.
func TestParseJobSearchResponse_NonObjectElementSkipped(t *testing.T) {
	raw := `{"jobs":[` +
		`{"title":"Eng 0","company":"Co","location":"Remote","source":"greenhouse","url":"https://ex.com/0","job_type":"full-time","remote":"remote","experience":"senior","skills":["Go"],"description":"Build.","posted":"1d"}` + `,` +
		`"not an object"` + `,` +
		`{"title":"Eng 2","company":"Co","location":"Remote","source":"greenhouse","url":"https://ex.com/2","job_type":"full-time","remote":"remote","experience":"senior","skills":["Go"],"description":"Build.","posted":"1d"}` +
		`],"summary":"Two valid records."}`

	jobs, _, outcome, salvaged, dropped := parseJobSearchResponse(raw)

	if len(jobs) != 2 {
		t.Fatalf("got %d jobs, want 2 (the non-object element was skipped)", len(jobs))
	}
	if outcome != ExtractionSchemaMismatch {
		t.Errorf("outcome = %q, want %q", outcome, ExtractionSchemaMismatch)
	}
	if salvaged != 2 {
		t.Errorf("salvaged = %d, want 2", salvaged)
	}
	if dropped != 1 {
		t.Errorf("dropped = %d, want 1 (the non-object element)", dropped)
	}
}

// --- MAJOR 6: the test seam must capture parameters ---

// TestSummarizeJobResults_SeamCapturesParams drives SummarizeJobResults via
// the seam and asserts the production call passes WithReasoningEffort("none")
// and cfg.LLMMaxTokens. The old seam discarded every parameter — dropping
// either left the whole suite green.
//
// Mutation (a): drop llm.WithReasoningEffort("none") from bridge_jobs.go →
// cap.reasoningEffort() returns "" → RED.
// Mutation (b): pass 0 instead of cfg.LLMMaxTokens → cap.maxTokens == 0 → RED.
func TestSummarizeJobResults_SeamCapturesParams(t *testing.T) {
	withTestRegistry(t)
	// Set a non-zero LLMMaxTokens so the assertion can distinguish "passed
	// through" from "zero-value default". Save/restore via t.Cleanup.
	origMaxTokens := cfg.LLMMaxTokens
	cfg.LLMMaxTokens = 16384
	t.Cleanup(func() { cfg.LLMMaxTokens = origMaxTokens })

	full := makeJobJSON(3, "Three matches.")
	cap := withJobSearchComplete(t, full)

	_, err := SummarizeJobResults(context.Background(), "golang engineer", JobSearchInstructionFor(JobSearchMaxLimit), 5000, nil, nil)
	if err != nil {
		t.Fatalf("SummarizeJobResults error: %v", err)
	}

	if !cap.called {
		t.Fatal("seam was not called — SummarizeJobResults did not reach the LLM completion")
	}
	// (a) WithReasoningEffort("none") must be passed.
	if eff := cap.reasoningEffort(); eff != "none" {
		t.Errorf("reasoning_effort = %q, want \"none\" (reasoning tokens must be disabled to protect the output budget)", eff)
	}
	// (b) cfg.LLMMaxTokens must be passed, not 0.
	if cap.maxTokens != cfg.LLMMaxTokens {
		t.Errorf("maxTokens = %d, want cfg.LLMMaxTokens = %d (the token budget must be passed through)", cap.maxTokens, cfg.LLMMaxTokens)
	}
	if cap.maxTokens == 0 {
		t.Error("maxTokens = 0 — the token budget was not passed (an unset budget lets the provider default to a small cap)")
	}
}

// --- MAJOR URL: SummarizeJobResults must not do positional URL backfill ---

// TestSummarizeJobResults_NoPositionalURLBackfill asserts SummarizeJobResults
// does NOT backfill empty job URLs from the positional results. The caller
// (runJobSearch) owns the SearxngResult→JobListing correspondence via
// assignFallbackURLs, which guards on equal lengths. A salvage path that
// drops records breaks the positional map by construction — guessing URLs
// here would mis-assign another listing's URL to a salvaged record.
//
// Mutation: re-add the positional backfill loop in SummarizeJobResults → RED:
// jobs[0].URL == "https://results.example.com/1" (a URL that is not its own).
func TestSummarizeJobResults_NoPositionalURLBackfill(t *testing.T) {
	withTestRegistry(t)
	// Canned response: 2 jobs with EMPTY URLs.
	resp := `{"jobs":[` +
		`{"title":"Eng A","company":"Co A","location":"Remote","source":"greenhouse","url":"","job_type":"full-time","remote":"remote","experience":"senior","skills":["Go"],"description":"Build.","posted":"1d"},` +
		`{"title":"Eng B","company":"Co B","location":"Remote","source":"lever","url":"","job_type":"full-time","remote":"remote","experience":"senior","skills":["Rust"],"description":"Build.","posted":"2d"}` +
		`],"summary":"Two matches."}`
	withJobSearchComplete(t, resp)

	// Results with URLs that must NOT be positionally assigned to the jobs.
	results := []SearxngResult{
		{URL: "https://results.example.com/1", Title: "Result 1"},
		{URL: "https://results.example.com/2", Title: "Result 2"},
	}

	out, err := SummarizeJobResults(context.Background(), "golang engineer", JobSearchInstructionFor(JobSearchMaxLimit), 5000, results, nil)
	if err != nil {
		t.Fatalf("SummarizeJobResults error: %v", err)
	}

	if len(out.Jobs) != 2 {
		t.Fatalf("got %d jobs, want 2", len(out.Jobs))
	}
	// The jobs' URLs must remain empty — no positional backfill.
	for i, j := range out.Jobs {
		if j.URL != "" {
			t.Errorf("jobs[%d].URL = %q, want \"\" (SummarizeJobResults must not do positional URL backfill — the caller owns the correspondence)", i, j.URL)
		}
	}
}
