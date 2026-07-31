package jobserver

import (
	"context"
	"errors"
	"testing"

	kitembed "github.com/anatolykoptev/go-kit/embed"
	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
)

// relevanceFakeEmbedder returns deterministic vectors so the relevance gate
// can be tested without a live embedder. The query vector is aligned with
// on-topic docs (high cosine) and orthogonal to off-topic docs (cosine ≈ 0).
//
// Vector scheme (3-dim, enough for cosine separation):
//   - query vector:       [1, 0, 0]
//   - on-topic passage:   [1, 0, 0]  → cosine = 1.0
//   - off-topic passage:  [0, 1, 0]  → cosine = 0.0
//
// EmbedQuery returns the query vector; Embed returns per-text vectors based
// on whether the text contains the on-topic marker "scraping".
type relevanceFakeEmbedder struct {
	queryVec []float32
}

func (f *relevanceFakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		if containsScraping(t) {
			out[i] = []float32{1, 0, 0} // on-topic: aligned with query
		} else {
			out[i] = []float32{0, 1, 0} // off-topic: orthogonal to query
		}
	}
	return out, nil
}

func (f *relevanceFakeEmbedder) EmbedQuery(_ context.Context, _ string) ([]float32, error) {
	return f.queryVec, nil
}

func (f *relevanceFakeEmbedder) Dimension() int { return len(f.queryVec) }
func (f *relevanceFakeEmbedder) Close() error   { return nil }

// relevanceErrorEmbedder always returns an error from Embed/EmbedQuery.
type relevanceErrorEmbedder struct{}

func (relevanceErrorEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return nil, errors.New("embedder unavailable")
}
func (relevanceErrorEmbedder) EmbedQuery(context.Context, string) ([]float32, error) {
	return nil, errors.New("embedder unavailable")
}
func (relevanceErrorEmbedder) Dimension() int { return 3 }
func (relevanceErrorEmbedder) Close() error   { return nil }

func containsScraping(s string) bool {
	for _, sub := range []string{"scraping", "anti-bot", "browser automation"} {
		if len(s) >= len(sub) && stringContains(s, sub) {
			return true
		}
	}
	return false
}

func stringContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// withRelevanceEmbedder sets the package-level embedder for the duration of
// the test and restores the previous value on cleanup.
func withRelevanceEmbedder(t *testing.T, e kitembed.Embedder) {
	t.Helper()
	prev := jobs.GetEmbedClient()
	jobs.SetEmbedClient(e)
	t.Cleanup(func() { jobs.SetEmbedClient(prev) })
}

// TestRelevanceGate_FiltersOffTopicResults is the core RED test for F1:
// given a mix of on-topic and off-topic results, the gate must reject the
// off-topic ones and keep only the on-topic ones.
//
// Falsification M1: make the gate return its input unchanged (no filtering).
// This test goes RED — off-topic results survive in the output.
func TestRelevanceGate_FiltersOffTopicResults(t *testing.T) {
	withRelevanceEmbedder(t, &relevanceFakeEmbedder{queryVec: []float32{1, 0, 0}})
	// Override the env-driven defaults for the test.
	origMin, origKeep := jobSearchMinRelevance, jobSearchMinKeep
	jobSearchMinRelevance = 0.5
	jobSearchMinKeep = 1
	t.Cleanup(func() { jobSearchMinRelevance, jobSearchMinKeep = origMin, origKeep })

	results := []engine.SearxngResult{
		{Title: "Web Scraping Engineer", URL: "http://example.com/1", Content: "anti-bot browser automation"},
		{Title: "Web Developer", URL: "http://example.com/2", Content: "frontend web development"},
		{Title: "Senior Automation Engineer", URL: "http://example.com/3", Content: "browser automation testing"},
		{Title: "Frontend Engineer", URL: "http://example.com/4", Content: "react frontend"},
	}

	got, degraded := applyRelevanceGate(context.Background(), "web scraping anti-bot", results)

	if degraded != "" {
		t.Fatalf("expected no degradation, got reason=%q", degraded)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 on-topic results kept, got %d: %+v", len(got), got)
	}
	for _, r := range got {
		if !containsScraping(r.Title + " " + r.Content) {
			t.Errorf("off-topic result survived the gate: %q", r.Title)
		}
	}
}

// TestRelevanceGate_FailOpenOnEmbedderError verifies the fail-open contract:
// when the embedder returns an error, results pass through UNFILTERED and a
// non-empty degraded reason is returned.
//
// Falsification M2: make the degrade path return nil/empty on embedder error.
// This test goes RED — either len(got)==0 or degraded=="".
func TestRelevanceGate_FailOpenOnEmbedderError(t *testing.T) {
	withRelevanceEmbedder(t, relevanceErrorEmbedder{})

	results := []engine.SearxngResult{
		{Title: "Job A", URL: "http://example.com/a", Content: "some job"},
		{Title: "Job B", URL: "http://example.com/b", Content: "another job"},
	}

	got, degraded := applyRelevanceGate(context.Background(), "test query", results)

	if len(got) != len(results) {
		t.Fatalf("fail-open must return all %d results unfiltered, got %d", len(results), len(got))
	}
	if degraded == "" {
		t.Fatal("fail-open must return a non-empty degraded reason, got empty")
	}
}

// TestRelevanceGate_FailOpenWhenNotConfigured verifies the not-configured
// fail-open path: when no embedder is set, results pass through unfiltered.
func TestRelevanceGate_FailOpenWhenNotConfigured(t *testing.T) {
	withRelevanceEmbedder(t, nil)

	results := []engine.SearxngResult{
		{Title: "Job A", URL: "http://example.com/a", Content: "some job"},
	}

	got, degraded := applyRelevanceGate(context.Background(), "test query", results)

	if len(got) != 1 {
		t.Fatalf("not-configured must return all results unfiltered, got %d", len(got))
	}
	if degraded != engine.RelevanceReasonNotConfigured {
		t.Fatalf("expected degraded reason %q, got %q", engine.RelevanceReasonNotConfigured, degraded)
	}
}

// TestRelevanceGate_ThresholdZeroKeepsEverything verifies that with threshold=0
// the gate keeps all results (none rejected). This is the control for M3:
// setting the threshold to 0 should make the rejection test fail.
//
// Falsification M3: the rejection test (TestRelevanceGate_FiltersOffTopicResults)
// goes RED when threshold=0 because nothing is rejected.
func TestRelevanceGate_ThresholdZeroKeepsEverything(t *testing.T) {
	withRelevanceEmbedder(t, &relevanceFakeEmbedder{queryVec: []float32{1, 0, 0}})
	origMin, origKeep := jobSearchMinRelevance, jobSearchMinKeep
	jobSearchMinRelevance = 0.0
	jobSearchMinKeep = 1
	t.Cleanup(func() { jobSearchMinRelevance, jobSearchMinKeep = origMin, origKeep })

	results := []engine.SearxngResult{
		{Title: "Web Scraping Engineer", URL: "http://example.com/1", Content: "anti-bot"},
		{Title: "Web Developer", URL: "http://example.com/2", Content: "frontend"},
	}

	got, degraded := applyRelevanceGate(context.Background(), "scraping", results)

	if degraded != "" {
		t.Fatalf("expected no degradation, got reason=%q", degraded)
	}
	if len(got) != 2 {
		t.Fatalf("threshold=0 must keep all %d results, got %d", len(results), len(got))
	}
}

// TestRelevanceGate_SortsByScoreDesc verifies the gate writes cosine scores
// into the Score field and returns results sorted by score descending.
func TestRelevanceGate_SortsByScoreDesc(t *testing.T) {
	withRelevanceEmbedder(t, &relevanceFakeEmbedder{queryVec: []float32{1, 0, 0}})
	origMin, origKeep := jobSearchMinRelevance, jobSearchMinKeep
	jobSearchMinRelevance = 0.0 // keep everything so we can inspect the order
	jobSearchMinKeep = 1
	t.Cleanup(func() { jobSearchMinRelevance, jobSearchMinKeep = origMin, origKeep })

	results := []engine.SearxngResult{
		{Title: "Off-topic", URL: "http://example.com/off", Content: "frontend"},        // cosine 0
		{Title: "On-topic", URL: "http://example.com/on", Content: "scraping anti-bot"}, // cosine 1
	}

	got, _ := applyRelevanceGate(context.Background(), "scraping", results)

	if len(got) != 2 {
		t.Fatalf("expected 2 results, got %d", len(got))
	}
	// On-topic (score=1.0) must come first.
	if got[0].Title != "On-topic" {
		t.Errorf("expected on-topic first (score=1.0), got %q (score=%.2f)", got[0].Title, got[0].Score)
	}
	if got[0].Score < 0.99 {
		t.Errorf("expected on-topic score ≈ 1.0, got %.4f", got[0].Score)
	}
	if got[1].Score > 0.01 {
		t.Errorf("expected off-topic score ≈ 0.0, got %.4f", got[1].Score)
	}
}

// TestRelevanceGate_EmptyInputReturnsEmpty verifies the gate is a no-op on
// empty input (no embedder call, no degradation).
func TestRelevanceGate_EmptyInputReturnsEmpty(t *testing.T) {
	got, degraded := applyRelevanceGate(context.Background(), "query", nil)
	if len(got) != 0 {
		t.Fatalf("expected empty output for empty input, got %d", len(got))
	}
	if degraded != "" {
		t.Fatalf("expected no degradation for empty input, got %q", degraded)
	}
}
