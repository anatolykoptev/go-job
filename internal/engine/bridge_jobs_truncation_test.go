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

	// F1.1: Summary must NOT contain the raw model output.
	if strings.Contains(out.Summary, cannedRaw) {
		t.Errorf("F1.1 FAIL: Summary must not contain raw model output; got: %q", out.Summary)
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
