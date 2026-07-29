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
