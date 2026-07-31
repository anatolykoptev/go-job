package jobserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs/connectors"
)

// testEmptySource returns (nil, nil) immediately — a genuine empty result.
type testEmptySource struct{}

func (testEmptySource) Name() string                        { return "test-empty" }
func (testEmptySource) Capabilities() connectors.Capability { return 0 }
func (testEmptySource) Groups() []string                    { return []string{"test-handler-platform"} }
func (testEmptySource) SiteScope() string                   { return "" }
func (testEmptySource) Fetch(_ context.Context, _ connectors.Query) ([]engine.SearxngResult, error) {
	return nil, nil
}

// testResultSource returns a fixed slice of results immediately.
type testResultSource struct {
	results []engine.SearxngResult
}

func (testResultSource) Name() string                        { return "test-result" }
func (testResultSource) Capabilities() connectors.Capability { return 0 }
func (testResultSource) Groups() []string                    { return []string{"test-handler-platform"} }
func (testResultSource) SiteScope() string                   { return "" }
func (s testResultSource) Fetch(_ context.Context, _ connectors.Query) ([]engine.SearxngResult, error) {
	return s.results, nil
}

// withTestRegistry swaps the package-level jobRegistry with a test registry
// containing the given sources, and restores the original on cleanup.
func withTestRegistry(t *testing.T, sources ...connectors.Source) {
	t.Helper()
	orig := jobRegistry
	r := connectors.New()
	for _, s := range sources {
		r.Register(s)
	}
	jobRegistry = r
	t.Cleanup(func() { jobRegistry = orig })
}

// assertSourcesInJSON marshals out and verifies the JSON contains "sources"
// and that len(out.Sources) > 0.
func assertSourcesInJSON(t *testing.T, out engine.JobSearchOutput, label string) {
	t.Helper()
	if len(out.Sources) == 0 {
		t.Fatalf("%s: len(out.Sources) = 0, want > 0 — Sources dropped from returned JobSearchOutput", label)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("%s: marshal: %v", label, err)
	}
	if !strings.Contains(string(raw), `"sources"`) {
		t.Fatalf("%s: marshalled JSON does not contain \"sources\" key — got: %s", label, string(raw))
	}
}

// TestJobSearchHandler_ZeroResults_SourcesReachOutput is the BLOCKER 6
// regression test for the zero-results path. The handler must populate
// out.Sources even when no results are found — the whole point of this PR is
// that the caller can see WHICH sources ran and what happened.
//
// Revert-red: delete the `Sources: sources` assignment in the zero-results
// return and this test goes RED (len(out.Sources) == 0).
func TestJobSearchHandler_ZeroResults_SourcesReachOutput(t *testing.T) {
	withTestRegistry(t, testEmptySource{})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	input := engine.JobSearchInput{
		Query:    "test-zero-results-unique-9f3a",
		Platform: "test-handler-platform",
	}

	_, out, err := runJobSearch(ctx, nil, input)
	if err != nil {
		t.Fatalf("zero-results handler returned error: %v", err)
	}
	assertSourcesInJSON(t, out, "zero-results path")
}

// TestJobSearchHandler_Success_SourcesReachOutput is the BLOCKER 6 regression
// test for the success path. It exercises the offset-beyond-total return — a
// legitimate success path (nil error, populated output) that is one of the
// four original sites that set Sources. The offset-beyond-total path is used
// because the LLM and fetch singletons are nil in the test environment (they
// are initialized by engine.Init, which is not called in tests), so the
// post-LLM path would panic on a nil dereference. The offset path reaches a
// `return ... Sources: sources` without touching the LLM/fetch singletons.
//
// Revert-red: delete the `Sources: sources` assignment in the offset-beyond-
// total return and this test goes RED (len(out.Sources) == 0).
func TestJobSearchHandler_Success_SourcesReachOutput(t *testing.T) {
	withTestRegistry(t, testResultSource{
		results: []engine.SearxngResult{
			{Title: "Go Developer", URL: "http://example.com/job1", Content: "Go job at TestCo"},
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	input := engine.JobSearchInput{
		Query:    "test-success-offset-unique-7e2b",
		Platform: "test-handler-platform",
		Offset:   100, // beyond len(deduped) → offset-beyond-total return with Sources
	}

	_, out, err := runJobSearch(ctx, nil, input)
	if err != nil {
		t.Fatalf("success handler returned error: %v", err)
	}
	assertSourcesInJSON(t, out, "success path (offset-beyond-total)")
}

// TestJobSearchHandler_B1_GateEmptiedSummaryReachOutput is the B1 end-to-end
// test: when the relevance gate rejects every candidate (minKeep=0, threshold
// above all scores), the handler returns an HONEST summary that names the
// gate — NOT the generic "No results found." (which misattributes the empty
// set to the sources) and NOT "offset beyond total" (which misattributes it to
// pagination). This path returns before the LLM (nil in tests), so it is
// testable without mocking SummarizeJobResults.
//
// It also covers the "gate verdict reaches the output summary" contract: the
// degraded/notice surfacing in tool_job_search.go. The fail-open degraded
// prefix (lines 363-364 / 375-376) needs the LLM path which is nil in tests
// (documented at line 96-99); this B1 path is the gate-to-summary wiring that
// IS reachable without the LLM.
//
// Revert-red: restore the unconditional positional URL fallback OR remove the
// B1 empty-gate early return → the summary no longer names the gate.
func TestJobSearchHandler_B1_GateEmptiedSummaryReachOutput(t *testing.T) {
	withTestRegistry(t, testResultSource{
		results: []engine.SearxngResult{
			{Title: "Web Developer", URL: "http://example.com/job1", Content: "frontend web development"},
			{Title: "Frontend Engineer", URL: "http://example.com/job2", Content: "react frontend"},
		},
	})
	// Fake embedder: query [1,0,0]; both candidates are off-topic ([0,1,0] →
	// cosine 0). Threshold 0.95 > 0 → all rejected. minKeep left at shipped
	// default 0 (B1 hard gate).
	withRelevanceEmbedder(t, &relevanceFakeEmbedder{queryVec: []float32{1, 0, 0}})
	origMin := jobSearchMinRelevance
	jobSearchMinRelevance = 0.95
	t.Cleanup(func() { jobSearchMinRelevance = origMin })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	input := engine.JobSearchInput{
		Query:    "web scraping anti-bot",
		Platform: "test-handler-platform",
	}

	_, out, err := runJobSearch(ctx, nil, input)
	if err != nil {
		t.Fatalf("B1 handler returned error: %v", err)
	}
	if !strings.Contains(out.Summary, "relevance threshold") {
		t.Fatalf("B1: summary must name the relevance gate, got %q", out.Summary)
	}
	if strings.Contains(out.Summary, "No results found.") {
		t.Fatalf("B1: summary must NOT misattribute the empty gate result to the sources, got %q", out.Summary)
	}
	if strings.Contains(out.Summary, "offset beyond total") {
		t.Fatalf("B1: summary must NOT misattribute the empty gate result to pagination, got %q", out.Summary)
	}
}
