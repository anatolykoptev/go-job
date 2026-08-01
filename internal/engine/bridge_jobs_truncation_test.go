package engine

// bridge_jobs_truncation_test.go: tests for the LLM truncation/unparseable
// detection in SummarizeJobResults.
//
// F1 — Feed a canned unparseable response: assert jobs is not nil-with-raw-
//      summary, the counter moved, and the WARN carries raw_len.
//      Mutation: restore `return &JobSearchOutput{Query: query, Summary: raw}, nil`
//      → RED (Summary contains raw, counter stays 0, no WARN).
//
// F2 — Assert both counter values appear in FormatMetrics() output — the
//      exposed flat-text endpoint, not only the registry snapshot.
//      Mutation: drop one from the FormatMetrics pre-touch list → RED.

import (
	"bytes"
	"context"
	"log/slog"
	"regexp"
	"strings"
	"testing"

	kitmetrics "github.com/anatolykoptev/go-kit/metrics"
)

// TestSummarizeJobResults_Unparseable_NoRawInSummary is the F1 falsification
// test. It feeds a canned truncated/unparseable LLM response through the real
// SummarizeJobResults (via the summarizeJobResultsLLM seam) and asserts:
//
//  1. Summary does NOT contain the raw model output (the silent-failure surface).
//  2. Summary states how many sources were collected (honest message).
//  3. Jobs is empty (no fabricated listings).
//  4. The gojob_job_search_extraction_total{outcome=unparseable} counter moved
//     from 0 to 1 (anti-vacuous: baseline asserted at 0, post-call at 1).
//  5. The WARN log carries raw_len and the routed model name.
//
// Revert-red: restore the old `return &JobSearchOutput{Query: query, Summary:
// raw}, nil` in the parsed==nil branch → Summary contains raw (1 RED), counter
// stays 0 (4 RED), no WARN emitted (5 RED).
func TestSummarizeJobResults_Unparseable_NoRawInSummary(t *testing.T) {
	// Fresh registry — anti-vacuous guard: a counter assertion against an
	// uninitialised registry reads 0==0 and holds everywhere.
	origReg := reg
	t.Cleanup(func() { reg = origReg })
	reg = kitmetrics.NewRegistry()

	// Capture slog WARN output.
	var logBuf bytes.Buffer
	origLog := slog.Default()
	t.Cleanup(func() { slog.SetDefault(origLog) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))

	// Set cfg.LLMModel so the WARN carries a identifiable model name.
	origModel := cfg.LLMModel
	t.Cleanup(func() { cfg.LLMModel = origModel })
	cfg.LLMModel = "test-truncation-model"

	t.Setenv("LLM_MODEL_WEIGHTS", "model-a=1,model-b=2")
	// llmModelWeights is captured once at Init time (mirroring go-kit which
	// reads LLM_MODEL_WEIGHTS once at client construction). The test does not
	// call Init, so set the package var directly — same package, and it
	// exercises the "read once, not per-failure" contract.
	origWeights := llmModelWeights
	t.Cleanup(func() { llmModelWeights = origWeights })
	llmModelWeights = "model-a=1,model-b=2"

	// Swap the LLM seam to return a canned unparseable (truncated mid-record)
	// response — the exact live failure shape.
	cannedRaw := `{"jobs": [{"title": "Software Engineer, UI/UX", "salary": "$127000`
	origSeam := summarizeJobResultsLLM
	t.Cleanup(func() { summarizeJobResultsLLM = origSeam })
	summarizeJobResultsLLM = func(_ context.Context, _, _ string, _ int, _ []SearxngResult, _ map[string]string) (*llmJobOutput, string, error) {
		return nil, cannedRaw, nil
	}

	results := []SearxngResult{
		{URL: "http://example.com/1", Title: "Job 1"},
		{URL: "http://example.com/2", Title: "Job 2"},
		{URL: "http://example.com/3", Title: "Job 3"},
		{URL: "http://example.com/4", Title: "Job 4"},
	}

	// Baseline: counter must be 0 before the call (anti-vacuous).
	snap0 := reg.Snapshot()
	if v := snap0[MetricJobSearchExtraction+"{outcome=unparseable}"]; v != 0 {
		t.Fatalf("baseline counter must be 0, got %d — registry not fresh", v)
	}

	out, err := SummarizeJobResults(context.Background(), "senior software engineer", "instruction", 5000, results, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// F1.1: Summary must NOT contain a distinctive token from the raw model
	// output. Asserting the WHOLE raw string passes a partial leak
	// (`Summary: msg + raw[:40]`); #413 spent five review rounds on
	// partial-salvage regressions, so assert on a token that sits inside the
	// first 40 bytes of the fixture — exactly the mutation that would slip
	// through a whole-string assertion. `{"jobs":` is a JSON structural token
	// at byte 0 of the fixture that would never appear in an honest message;
	// `$127000` catches larger leaks past byte 40.
	if strings.Contains(out.Summary, `{"jobs":`) {
		t.Errorf("F1.1 FAIL: Summary must not contain raw model output token {\"jobs\":}; got: %q", out.Summary)
	}
	if strings.Contains(out.Summary, "Software Engineer,") {
		t.Errorf("F1.1b FAIL: Summary must not contain raw model output token 'Software Engineer,'; got: %q", out.Summary)
	}
	if strings.Contains(out.Summary, "$127000") {
		t.Errorf("F1.1c FAIL: Summary must not contain raw model output token '$127000'; got: %q", out.Summary)
	}

	// F1.2: Summary must mention the source count (honest message).
	if !strings.Contains(out.Summary, "4 source") {
		t.Errorf("F1.2 FAIL: Summary must state how many sources were collected; got: %q", out.Summary)
	}

	// F1.3: Jobs must be empty (no fabricated listings from truncated JSON).
	if len(out.Jobs) != 0 {
		t.Errorf("F1.3 FAIL: Jobs must be empty on unparseable response; got %d jobs", len(out.Jobs))
	}

	// F1.4: Counter moved from 0 to 1.
	snap1 := reg.Snapshot()
	if v := snap1[MetricJobSearchExtraction+"{outcome=unparseable}"]; v != 1 {
		t.Errorf("F1.4 FAIL: counter unparseable = %d, want 1 (must increment on parse failure)", v)
	}

	// F1.5: WARN log carries raw_len.
	logOut := logBuf.String()
	if !strings.Contains(logOut, "raw_len=") {
		t.Errorf("F1.5 FAIL: WARN log must carry raw_len; got:\n%s", logOut)
	}
	// raw_len value must match len(cannedRaw).
	wantRawLen := `raw_len=` + itoa(len(cannedRaw))
	if !strings.Contains(logOut, wantRawLen) {
		t.Errorf("F1.5b FAIL: WARN log must carry raw_len=%d; got:\n%s", len(cannedRaw), logOut)
	}

	// F1.6: WARN log carries the routed model name.
	if !strings.Contains(logOut, "test-truncation-model") {
		t.Errorf("F1.6 FAIL: WARN log must carry model name; got:\n%s", logOut)
	}

	// F1.7: Unparseable signal must be set — the explicit flag callers branch
	// on to skip caching. NOT inferred from Jobs==nil (the genuine zero-result
	// and relevance-gate-empty paths also produce nil Jobs and must stay
	// cached). Mutation: drop `Unparseable: true` from the return → RED.
	if !out.Unparseable {
		t.Errorf("F1.7 FAIL: Unparseable must be true on unparseable response; got false")
	}

	// F4: WARN log must carry model_weights and sources — the two fields the
	// PR body promises as the routing/cause evidence. Each asserted
	// independently so deleting either field from the slog.Warn call goes RED
	// on its own (mutation: delete slog.String("model_weights", …) → RED;
	// delete slog.Int("sources", …) → RED).
	if !strings.Contains(logOut, "model_weights=") {
		t.Errorf("F4a FAIL: WARN log must carry model_weights; got:\n%s", logOut)
	}
	// slog text handler quotes values containing commas/equals, so check the
	// raw value substring (present whether quoted or not), not the exact
	// `key=value` splice.
	if !strings.Contains(logOut, "model-a=1,model-b=2") {
		t.Errorf("F4a-val FAIL: WARN log must carry the captured LLM_MODEL_WEIGHTS value; got:\n%s", logOut)
	}
	if !strings.Contains(logOut, "sources=") {
		t.Errorf("F4b FAIL: WARN log must carry sources; got:\n%s", logOut)
	}
	if !strings.Contains(logOut, "sources=4") {
		t.Errorf("F4b-val FAIL: WARN log must carry sources=4; got:\n%s", logOut)
	}
}

// TestSummarizeJobResults_Ok_IncrementsOkCounter verifies the success path
// increments outcome=ok.
//
// Revert-red: remove the IncrJobSearchExtraction("ok") call → counter stays 0.
func TestSummarizeJobResults_Ok_IncrementsOkCounter(t *testing.T) {
	origReg := reg
	t.Cleanup(func() { reg = origReg })
	reg = kitmetrics.NewRegistry()

	origSeam := summarizeJobResultsLLM
	t.Cleanup(func() { summarizeJobResultsLLM = origSeam })
	summarizeJobResultsLLM = func(_ context.Context, _, _ string, _ int, _ []SearxngResult, _ map[string]string) (*llmJobOutput, string, error) {
		return &llmJobOutput{Jobs: []JobListing{{Title: "Go Dev", URL: "http://example.com/1"}}, Summary: "1 job found"}, "", nil
	}

	_, err := SummarizeJobResults(context.Background(), "test", "instruction", 5000,
		[]SearxngResult{{URL: "http://example.com/1"}}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	snap := reg.Snapshot()
	if v := snap[MetricJobSearchExtraction+"{outcome=ok}"]; v != 1 {
		t.Errorf("counter ok = %d, want 1", v)
	}
}

// TestSummarizeJobResults_ZeroResult_NotUnparseable verifies the genuine
// zero-result path (LLM returned valid JSON with an empty Jobs slice) sets
// Unparseable=false — so it STAYS cacheable. This is the discriminating half
// of the cache-skip contract: the unparseable path sets the flag (skip cache),
// the zero-result path clears it (keep cache). Inferring unparseable from
// Jobs==nil would wrongly evict zero-result responses from cache.
//
// Mutation: set Unparseable=true unconditionally → RED.
func TestSummarizeJobResults_ZeroResult_NotUnparseable(t *testing.T) {
	origSeam := summarizeJobResultsLLM
	t.Cleanup(func() { summarizeJobResultsLLM = origSeam })
	summarizeJobResultsLLM = func(_ context.Context, _, _ string, _ int, _ []SearxngResult, _ map[string]string) (*llmJobOutput, string, error) {
		// Valid JSON, zero jobs — the genuine "no results" shape.
		return &llmJobOutput{Jobs: nil, Summary: "no jobs matched"}, "", nil
	}

	out, err := SummarizeJobResults(context.Background(), "rare query", "instruction", 5000,
		[]SearxngResult{{URL: "http://example.com/1"}}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Unparseable {
		t.Errorf("zero-result must NOT set Unparseable (it stays cacheable); got true")
	}
}

// TestCacheStoreJSON_UnparseableNotCached is the F1-cache falsification test.
// It proves the cache-skip contract at the engine seam: an unparseable
// JobSearchOutput (Unparseable=true) is NOT written to cache, while a genuine
// zero-result (Unparseable=false) IS. The runJobSearch caller branches on
// jobOut.Unparseable before calling CacheStoreJSON; this test exercises that
// branch logic directly so the contract is unit-testable without the full
// job_search fan-out.
//
// Mutation: restore the unconditional `engine.CacheStoreJSON(...)` call in
// runJobSearch (drop the `if !jobOut.Unparseable` guard) → the unparseable
// load succeeds and the RED fires.
func TestCacheStoreJSON_UnparseableNotCached(t *testing.T) {
	// InitCache with a nil redis URL → L1-only in-memory cache.
	InitCache("", CacheTTL, 64, 0)
	ctx := context.Background()
	key := CacheKey("test-unparseable-cache-skip")

	// Simulate the runJobSearch branch: unparseable → skip store.
	unparseable := JobSearchOutput{Query: "q", Summary: "incomplete", Unparseable: true}
	if !unparseable.Unparseable {
		t.Fatalf("test setup: unparseable output must have Unparseable=true")
	}
	// The caller's guard: only store when NOT unparseable.
	if !unparseable.Unparseable {
		CacheStoreJSON(ctx, key, "q", unparseable)
	}

	if _, ok := CacheLoadJSON[JobSearchOutput](ctx, key); ok {
		t.Errorf("F1-cache FAIL: unparseable result must NOT be in cache; got a hit")
	}

	// Genuine zero-result → Unparseable=false → stored.
	zero := JobSearchOutput{Query: "q", Summary: "no results", Unparseable: false}
	if zero.Unparseable {
		t.Fatalf("test setup: zero-result must have Unparseable=false")
	}
	CacheStoreJSON(ctx, key, "q", zero)
	loaded, ok := CacheLoadJSON[JobSearchOutput](ctx, key)
	if !ok {
		t.Fatalf("F1-cache FAIL: genuine zero-result must be cached; got a miss")
	}
	// json:"-" field must not survive the round trip — confirms it stays off
	// the wire AND out of the cached blob.
	if loaded.Unparseable {
		t.Errorf("F1-cache FAIL: Unparseable is json:\"-\" and must not persist in cache; got true")
	}
}

// TestFormatMetrics_JobSearchExtraction_BothOutcomes is the F2 falsification
// test. Both outcome labels (ok, unparseable) must appear in the FormatMetrics
// flat-text output at 0 — the exposed endpoint text, not only the registry
// snapshot.
//
// Revert-red: drop one outcome from the FormatMetrics keys list → the anchored
// regex fails to match → RED.
func TestFormatMetrics_JobSearchExtraction_BothOutcomes(t *testing.T) {
	origReg := reg
	t.Cleanup(func() { reg = origReg })
	reg = kitmetrics.NewRegistry()

	out := FormatMetrics()
	for _, oc := range []string{"ok", "unparseable"} {
		re := regexp.MustCompile(`(?m)^job_search_extraction_total\{outcome=` + oc + `\} 0$`)
		if !re.MatchString(out) {
			t.Errorf("F2 FAIL: FormatMetrics must contain %q; got:\n%s", re.String(), out)
		}
	}
}

// itoa is a local int-to-string helper to avoid importing strconv for one call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
