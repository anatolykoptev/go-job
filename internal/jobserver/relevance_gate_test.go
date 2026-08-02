package jobserver

import (
	"context"
	"errors"
	"testing"
	"time"

	kitembed "github.com/anatolykoptev/go-kit/embed"
	"github.com/anatolykoptev/go-kit/retry"
	"github.com/anatolykoptev/go_job/internal/engine"
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
// This WIDE margin (1.0) is used for the mechanic tests (sorting, threshold=0
// keeps all). The default-value regression tests use relevanceNarrowEmbedder
// whose margin is realistic (~0.03, matching the measured e5-large gap).
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

// relevanceNarrowEmbedder produces a REALISTIC margin (~0.03) between on-topic
// and off-topic candidates, matching the measured e5-large same-query gap
// (off-topic 0.7935 → on-topic 0.8269 = 0.033, per ~/tmp/relevance_before.txt).
// The wide-margin fake above separates 1.0 from 0.0, so it CANNOT tell a
// 0.80 threshold from 0.05 — exactly why B1 survived a green suite. This fake
// makes the default-value regression tests sensitive to the actual threshold.
//
// Vector scheme (2-dim, query = [1, 0]):
//   - on-topic passage:  [0.86, 0.51]  → cosine ≈ 0.86
//   - off-topic passage: [0.83, 0.56]  → cosine ≈ 0.83
//
// margin ≈ 0.03 — a threshold of 0.845 separates them; 0.95 rejects both.
type relevanceNarrowEmbedder struct {
	queryVec []float32
}

func (f *relevanceNarrowEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		if containsScraping(t) {
			out[i] = []float32{0.86, 0.51} // on-topic: cosine ≈ 0.86
		} else {
			out[i] = []float32{0.83, 0.56} // off-topic: cosine ≈ 0.83
		}
	}
	return out, nil
}

func (f *relevanceNarrowEmbedder) EmbedQuery(_ context.Context, _ string) ([]float32, error) {
	return f.queryVec, nil
}

func (f *relevanceNarrowEmbedder) Dimension() int { return len(f.queryVec) }
func (f *relevanceNarrowEmbedder) Close() error   { return nil }

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

// relevanceCircuitOpenEmbedder returns kitembed.ErrCircuitOpen — the
// circuit_open branch of classifyEmbedError (M1: previously never tested).
type relevanceCircuitOpenEmbedder struct{}

func (relevanceCircuitOpenEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return nil, kitembed.ErrCircuitOpen
}
func (relevanceCircuitOpenEmbedder) EmbedQuery(context.Context, string) ([]float32, error) {
	return nil, kitembed.ErrCircuitOpen
}
func (relevanceCircuitOpenEmbedder) Dimension() int { return 3 }
func (relevanceCircuitOpenEmbedder) Close() error   { return nil }

// relevanceTimeoutEmbedder blocks until the gate's own context deadline fires,
// exercising the timeout branch of classifyEmbedError (M1: previously never
// tested). The gate uses a 15s default timeout; tests shrink it via
// jobSearchRelevanceTimeout so this returns well before the test deadline.
type relevanceTimeoutEmbedder struct{}

func (relevanceTimeoutEmbedder) Embed(ctx context.Context, _ []string) ([][]float32, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (relevanceTimeoutEmbedder) EmbedQuery(ctx context.Context, _ string) ([]float32, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (relevanceTimeoutEmbedder) Dimension() int { return 3 }
func (relevanceTimeoutEmbedder) Close() error   { return nil }

// relevancePerItemEmptyEmbedder returns a FULL-LENGTH response (len(vecs) ==
// len(texts)) but with one per-item vector empty. Without the M5 check, the
// empty EmbedVector stays nil, MathReranker scores it 0, and the item is
// silently dropped as "irrelevant" with degraded="". With the check, the gate
// treats it as degradation (fail-open).
type relevancePerItemEmptyEmbedder struct {
	emptyIndex int
}

func (e *relevancePerItemEmptyEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		if i == e.emptyIndex {
			out[i] = nil // per-item empty vector (M5)
			continue
		}
		out[i] = []float32{1, 0, 0}
	}
	return out, nil
}

func (e *relevancePerItemEmptyEmbedder) EmbedQuery(_ context.Context, _ string) ([]float32, error) {
	return []float32{1, 0, 0}, nil
}

func (e *relevancePerItemEmptyEmbedder) Dimension() int { return 3 }
func (e *relevancePerItemEmptyEmbedder) Close() error   { return nil }

// relevancePartialBatchEmbedder returns fewer vectors than inputs (partial
// batch) — the len(vecs) < len(results) fail-open path.
type relevancePartialBatchEmbedder struct{}

func (relevancePartialBatchEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	if len(out) > 1 {
		out = out[:len(out)-1] // drop the last vector
	}
	for i := range out {
		out[i] = []float32{1, 0, 0}
	}
	return out, nil
}

func (relevancePartialBatchEmbedder) EmbedQuery(_ context.Context, _ string) ([]float32, error) {
	return []float32{1, 0, 0}, nil
}

func (relevancePartialBatchEmbedder) Dimension() int { return 3 }
func (relevancePartialBatchEmbedder) Close() error   { return nil }

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

// withRelevanceEmbedder sets the relevance gate's OWN embed client for the
// duration of the test and restores the previous value on cleanup. This sets
// the gate-scoped client (relevanceEmbedClient), NOT the jobs package-level
// singleton — the gate reads getRelevanceEmbedClient, so fakes must install
// there. The shared singleton (jobs.SetEmbedClient) is left untouched so
// non-gate consumers keep their own wiring in their own tests.
func withRelevanceEmbedder(t *testing.T, e kitembed.Embedder) {
	t.Helper()
	prev := relevanceEmbedClient
	relevanceEmbedClient = e
	t.Cleanup(func() { relevanceEmbedClient = prev })
}

// withRelevanceConfig overrides the gate config for the test and restores on
// cleanup. Pass sentinel values to leave a field unchanged.
func withRelevanceConfig(t *testing.T, minRelevance float64, minKeep int) {
	t.Helper()
	origMin, origKeep := jobSearchMinRelevance, jobSearchMinKeep
	if minRelevance >= 0 {
		jobSearchMinRelevance = minRelevance
	}
	if minKeep >= 0 {
		jobSearchMinKeep = minKeep
	}
	t.Cleanup(func() { jobSearchMinRelevance, jobSearchMinKeep = origMin, origKeep })
}

// TestRelevanceGate_FiltersOffTopicResults is the core mechanic test:
// given a mix of on-topic and off-topic results, the gate must reject the
// off-topic ones and keep only the on-topic ones.
//
// Falsification M1: make the gate return its input unchanged (no filtering).
// This test goes RED — off-topic results survive in the output.
func TestRelevanceGate_FiltersOffTopicResults(t *testing.T) {
	withRelevanceEmbedder(t, &relevanceFakeEmbedder{queryVec: []float32{1, 0, 0}})
	withRelevanceConfig(t, 0.5, 1)

	results := []engine.SearxngResult{
		{Title: "Web Scraping Engineer", URL: "http://example.com/1", Content: "anti-bot browser automation"},
		{Title: "Web Developer", URL: "http://example.com/2", Content: "frontend web development"},
		{Title: "Senior Automation Engineer", URL: "http://example.com/3", Content: "browser automation testing"},
		{Title: "Frontend Engineer", URL: "http://example.com/4", Content: "react frontend"},
	}

	got, degraded, notice := applyRelevanceGate(context.Background(), "web scraping anti-bot", results)

	if degraded != "" {
		t.Fatalf("expected no degradation, got reason=%q", degraded)
	}
	if notice != "" {
		t.Fatalf("expected no notice, got %q", notice)
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
	withRelevanceConfig(t, 0.5, 1)

	results := []engine.SearxngResult{
		{Title: "Job A", URL: "http://example.com/a", Content: "some job"},
		{Title: "Job B", URL: "http://example.com/b", Content: "another job"},
	}

	got, degraded, _ := applyRelevanceGate(context.Background(), "test query", results)

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
	withRelevanceConfig(t, 0.5, 1)

	results := []engine.SearxngResult{
		{Title: "Job A", URL: "http://example.com/a", Content: "some job"},
	}

	got, degraded, _ := applyRelevanceGate(context.Background(), "test query", results)

	if len(got) != 1 {
		t.Fatalf("not-configured must return all results unfiltered, got %d", len(got))
	}
	if degraded != engine.RelevanceReasonNotConfigured {
		t.Fatalf("expected degraded reason %q, got %q", engine.RelevanceReasonNotConfigured, degraded)
	}
}

// TestRelevanceGate_ThresholdZeroKeepsEverything verifies that with threshold=0
// the gate keeps all results (none rejected). This is the SHIPPED DEFAULT
// threshold (M1: 0.80 was not defensible; default is now 0.0).
//
// Falsification M3: the rejection test (TestRelevanceGate_FiltersOffTopicResults)
// goes RED when threshold=0 because nothing is rejected.
func TestRelevanceGate_ThresholdZeroKeepsEverything(t *testing.T) {
	withRelevanceEmbedder(t, &relevanceFakeEmbedder{queryVec: []float32{1, 0, 0}})
	withRelevanceConfig(t, 0.0, 1)

	results := []engine.SearxngResult{
		{Title: "Web Scraping Engineer", URL: "http://example.com/1", Content: "anti-bot"},
		{Title: "Web Developer", URL: "http://example.com/2", Content: "frontend"},
	}

	got, degraded, _ := applyRelevanceGate(context.Background(), "scraping", results)

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
	withRelevanceConfig(t, 0.0, 1) // keep everything so we can inspect the order

	results := []engine.SearxngResult{
		{Title: "Off-topic", URL: "http://example.com/off", Content: "frontend"},        // cosine 0
		{Title: "On-topic", URL: "http://example.com/on", Content: "scraping anti-bot"}, // cosine 1
	}

	got, _, _ := applyRelevanceGate(context.Background(), "scraping", results)

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
	got, degraded, notice := applyRelevanceGate(context.Background(), "query", nil)
	if len(got) != 0 {
		t.Fatalf("expected empty output for empty input, got %d", len(got))
	}
	if degraded != "" {
		t.Fatalf("expected no degradation for empty input, got %q", degraded)
	}
	if notice != "" {
		t.Fatalf("expected no notice for empty input, got %q", notice)
	}
}

// === B1 regression test — the gate must NOT hand back what it rejected ===
//
// At the SHIPPED DEFAULT minKeep=0 (a true hard gate), when every candidate
// scores below the threshold the gate returns EMPTY — not the top-N rejected
// items. The prior default minKeep=3 turned "8 wrong answers" into "3 wrong
// answers" and reported success.
//
// This test uses a realistic-margin embedder (relevanceNarrowEmbedder, ~0.03
// gap) and a threshold (0.95) above both classes so ALL candidates are below
// it. It does NOT override minKeep — it relies on the shipped default 0.
//
// Falsification N1: set the min-keep default back to a positive floor (3).
// The gate returns 3 floor-survivors instead of empty → this test goes RED.
func TestRelevanceGate_B1_AllBelowThresholdReturnsEmpty(t *testing.T) {
	withRelevanceEmbedder(t, &relevanceNarrowEmbedder{queryVec: []float32{1, 0}})
	// Threshold above both classes (0.86 / 0.83) → all rejected. minKeep is
	// NOT overridden: the test relies on the shipped default 0 (B1).
	origMin := jobSearchMinRelevance
	jobSearchMinRelevance = 0.95
	t.Cleanup(func() { jobSearchMinRelevance = origMin })

	results := []engine.SearxngResult{
		{Title: "Web Scraping Engineer", URL: "http://example.com/1", Content: "anti-bot browser automation"},
		{Title: "Web Developer", URL: "http://example.com/2", Content: "frontend web development"},
		{Title: "Senior Automation Engineer", URL: "http://example.com/3", Content: "browser automation testing"},
		{Title: "Frontend Engineer", URL: "http://example.com/4", Content: "react frontend"},
	}

	got, degraded, notice := applyRelevanceGate(context.Background(), "web scraping anti-bot", results)

	if degraded != "" {
		t.Fatalf("gate ran successfully (all below threshold), expected degraded=\"\", got %q", degraded)
	}
	if notice != "" {
		t.Fatalf("no floor should engage at minKeep=0, expected notice=\"\", got %q", notice)
	}
	if len(got) != 0 {
		t.Fatalf("B1: hard gate (minKeep=0) must return EMPTY when all candidates are below threshold; got %d results (the gate handed back what it rejected): %+v", len(got), got)
	}
}

// === B2 test — the floor must be a DISTINCT, observable state ===
//
// When an operator sets minKeep > 0 and fewer than minKeep candidates pass the
// threshold, the floor survivors are flagged as a distinct state: a non-empty
// notice string (caller-visible) AND they are NOT counted as ordinary `kept`.
// The notice tells the user these results did not meet the relevance bar.
//
// Falsification N2: remove the floor's distinct outcome so floor-survivors
// count as `kept` (no notice). This test goes RED — notice is empty.
func TestRelevanceGate_B2_FloorEngagedIsObservable(t *testing.T) {
	withRelevanceEmbedder(t, &relevanceNarrowEmbedder{queryVec: []float32{1, 0}})
	// Threshold 0.845 sits in the 0.03 gap: on-topic (0.86) passes, off-topic
	// (0.83) does not. With minKeep=3 and only 1 on-topic candidate, the floor
	// engages and keeps 2 off-topic survivors.
	withRelevanceConfig(t, 0.845, 3)

	results := []engine.SearxngResult{
		{Title: "Web Scraping Engineer", URL: "http://example.com/1", Content: "anti-bot browser automation"},     // on-topic 0.86
		{Title: "Web Developer", URL: "http://example.com/2", Content: "frontend web development"},                // off-topic 0.83
		{Title: "Senior Automation Engineer", URL: "http://example.com/3", Content: "browser automation testing"}, // on-topic 0.86
		{Title: "Frontend Engineer", URL: "http://example.com/4", Content: "react frontend"},                      // off-topic 0.83
	}

	got, degraded, notice := applyRelevanceGate(context.Background(), "web scraping anti-bot", results)

	if degraded != "" {
		t.Fatalf("gate ran successfully, expected degraded=\"\", got %q", degraded)
	}
	// 2 on-topic pass (0.86 >= 0.845); floor keeps top-3 by score → 2 passing + 1 floor survivor.
	if len(got) != 3 {
		t.Fatalf("expected 3 results (2 passing + 1 floor survivor), got %d: %+v", len(got), got)
	}
	// B2: the floor MUST surface a distinct, non-empty notice.
	if notice == "" {
		t.Fatal("B2: floor engaged but notice is empty — floor survivors are silently indistinguishable from threshold-passers")
	}
	if !stringContains(notice, "floor") {
		t.Fatalf("B2: notice must mention the floor, got %q", notice)
	}
}

// === The counter invariant: scored == kept + floor_kept + rejected ===

// relevanceOutcomeDeltas returns how much each outcome counter moved across fn.
func relevanceOutcomeDeltas(fn func()) map[string]int64 {
	key := func(outcome string) string {
		return engine.MetricJobSearchRelevance + "{outcome=" + outcome + "}"
	}
	before := engine.GetMetrics()
	fn()
	after := engine.GetMetrics()

	out := map[string]int64{}
	for _, oc := range []string{
		engine.RelevanceScored, engine.RelevanceKept,
		engine.RelevanceFloorKept, engine.RelevanceRejected,
	} {
		out[oc] = after[key(oc)] - before[key(oc)]
	}
	return out
}

// Every candidate the gate scores must be accounted for by exactly one outcome.
// Under the N1 bug this did not hold — scored=2 with kept, floor_kept and
// rejected all 0 — so this assertion alone would have caught it without anyone
// writing the fewer-candidates-than-floor case. That is the point: a test per
// bug does not end the pattern of blockers hiding in unreached branches.
func TestRelevanceGate_CounterInvariantHoldsOnEveryPath(t *testing.T) {
	engine.InitTestRegistry()

	onTopicA := engine.SearxngResult{Title: "Web Scraping Engineer", URL: "http://example.com/1", Content: "anti-bot browser automation"}
	onTopicB := engine.SearxngResult{Title: "Senior Automation Engineer", URL: "http://example.com/3", Content: "browser automation testing"}
	offTopicA := engine.SearxngResult{Title: "Web Developer", URL: "http://example.com/2", Content: "frontend web development"}
	offTopicB := engine.SearxngResult{Title: "Frontend Engineer", URL: "http://example.com/4", Content: "react frontend"}

	cases := []struct {
		name         string
		minRelevance float64
		minKeep      int
		results      []engine.SearxngResult
	}{
		{"shipped defaults — nothing rejected", 0.0, 0, []engine.SearxngResult{onTopicA, offTopicA, onTopicB, offTopicB}},
		{"hard gate — everything rejected", 0.95, 0, []engine.SearxngResult{onTopicA, offTopicA, onTopicB, offTopicB}},
		{"floor branch A — some pass", 0.845, 3, []engine.SearxngResult{onTopicA, offTopicA, onTopicB, offTopicB}},
		{"floor branch B — none pass", 0.95, 3, []engine.SearxngResult{offTopicA, offTopicB}},
		{"floor branch B — all pass, floor must not fire", 0.5, 5, []engine.SearxngResult{onTopicA, offTopicA, onTopicB}},
		{"no floor configured, partial pass", 0.845, 0, []engine.SearxngResult{onTopicA, offTopicA}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withRelevanceEmbedder(t, &relevanceNarrowEmbedder{queryVec: []float32{1, 0}})
			withRelevanceConfig(t, tc.minRelevance, tc.minKeep)

			var degraded string
			d := relevanceOutcomeDeltas(func() {
				_, degraded, _ = applyRelevanceGate(context.Background(), "web scraping anti-bot", tc.results)
			})
			if degraded != "" {
				t.Fatalf("gate degraded (%q) — counters would not be emitted, case is vacuous", degraded)
			}

			scored := d[engine.RelevanceScored]
			sum := d[engine.RelevanceKept] + d[engine.RelevanceFloorKept] + d[engine.RelevanceRejected]
			if scored == 0 {
				t.Fatalf("scored delta is 0 — the registry is not recording, so this assertion proves nothing")
			}
			if scored != sum {
				t.Fatalf("counter invariant violated: scored=%d != kept=%d + floor_kept=%d + rejected=%d (sum %d) — some candidate is attributed to no outcome",
					scored, d[engine.RelevanceKept], d[engine.RelevanceFloorKept], d[engine.RelevanceRejected], sum)
			}
		})
	}
}

// The floor must stay silent when it did not floor anything. FilterByScore
// returns the whole slice whenever there are fewer candidates than the floor —
// including when every one of them passed. Telling the caller those results
// "did not meet the threshold" would be a lie about genuine matches, and no
// existing test would catch it.
//
// Dropping `passingCount < jobSearchMinKeep` from floorEngaged must turn this RED.
func TestRelevanceGate_FloorSilentWhenEverythingPasses(t *testing.T) {
	withRelevanceEmbedder(t, &relevanceNarrowEmbedder{queryVec: []float32{1, 0}})
	// Floor of 5 over 3 candidates takes FilterByScore's second branch, but the
	// threshold is low enough that all three pass on merit.
	withRelevanceConfig(t, 0.5, 5)

	results := []engine.SearxngResult{
		{Title: "Web Scraping Engineer", URL: "http://example.com/1", Content: "anti-bot browser automation"},
		{Title: "Web Developer", URL: "http://example.com/2", Content: "frontend web development"},
		{Title: "Senior Automation Engineer", URL: "http://example.com/3", Content: "browser automation testing"},
	}

	got, degraded, notice := applyRelevanceGate(context.Background(), "web scraping anti-bot", results)

	if degraded != "" {
		t.Fatalf("gate ran successfully, expected degraded=\"\", got %q", degraded)
	}
	if len(got) != 3 {
		t.Fatalf("all three pass the threshold, expected 3, got %d", len(got))
	}
	if notice != "" {
		t.Fatalf("floor did not floor anything, but the caller was told it did: %q", notice)
	}
}

// === N1: the OTHER floor branch — fewer candidates than the floor ===

// FilterByScore floors in two different ways, and the second one was invisible.
// With minKeep greater than the candidate count it returns the WHOLE slice, so
// every rejected result came back to the caller with no notice and no distinct
// counter — B1 and B2 again, in the one configuration the floor exists for.
//
// Reverting the fix (restoring `&& len(sorted) >= jobSearchMinKeep` to the
// floorEngaged predicate) must turn this RED.
func TestRelevanceGate_N1_FloorEngagedWhenFewerCandidatesThanFloor(t *testing.T) {
	withRelevanceEmbedder(t, &relevanceNarrowEmbedder{queryVec: []float32{1, 0}})
	// Threshold above everything the embedder can produce: nothing passes.
	// minKeep=3 with only 2 candidates takes FilterByScore's second branch.
	withRelevanceConfig(t, 0.95, 3)

	results := []engine.SearxngResult{
		{Title: "Web Developer", URL: "http://example.com/2", Content: "frontend web development"},
		{Title: "Frontend Engineer", URL: "http://example.com/4", Content: "react frontend"},
	}

	got, degraded, notice := applyRelevanceGate(context.Background(), "web scraping anti-bot", results)

	if degraded != "" {
		t.Fatalf("gate ran successfully, expected degraded=\"\", got %q", degraded)
	}
	if len(got) != 2 {
		t.Fatalf("FilterByScore returns the whole slice below the floor, expected 2, got %d", len(got))
	}
	// The results are BELOW the threshold. Handing them back silently is the bug.
	if notice == "" {
		t.Fatal("N1: floor engaged via the fewer-candidates-than-floor branch, but notice is empty — the caller cannot tell rejected results from matches")
	}
	if !stringContains(notice, "floor") {
		t.Fatalf("N1: notice must mention the floor, got %q", notice)
	}
}

// === M1: classifyEmbedError on both branches (previously never tested) ===
//
// The classifier now takes the gate context so it can distinguish whose
// deadline expired via context.Cause (Change 1, #452). A bare
// context.DeadlineExceeded with an uncancelled gateCtx (Cause == nil) classifies
// as timeout_parent — the gate's own budget did not fire.

func TestClassifyEmbedError_CircuitOpen(t *testing.T) {
	got := classifyEmbedError(kitembed.ErrCircuitOpen, context.Background())
	if got != engine.RelevanceReasonCircuitOpen {
		t.Fatalf("classifyEmbedError(ErrCircuitOpen) = %q, want %q", got, engine.RelevanceReasonCircuitOpen)
	}
}

func TestClassifyEmbedError_DeadlineIsParentWhenGateNotExpired(t *testing.T) {
	// A bare DeadlineExceeded with an uncancelled gate context: the gate's
	// own budget did not fire (Cause == nil), so this is the parent's
	// deadline (or a non-gate deadline) → timeout_parent.
	got := classifyEmbedError(context.DeadlineExceeded, context.Background())
	if got != engine.RelevanceReasonTimeoutParent {
		t.Fatalf("classifyEmbedError(DeadlineExceeded, unexpired gate) = %q, want %q", got, engine.RelevanceReasonTimeoutParent)
	}
}

func TestClassifyEmbedError_CanceledIsParentTimeout(t *testing.T) {
	// Canceled with an uncancelled gate context → timeout_parent. (The
	// Canceled-as-timeout conflation is a separately filed bug; this test
	// pins the CURRENT behaviour so the gate/parent split does not regress it.)
	got := classifyEmbedError(context.Canceled, context.Background())
	if got != engine.RelevanceReasonTimeoutParent {
		t.Fatalf("classifyEmbedError(Canceled, unexpired gate) = %q, want %q", got, engine.RelevanceReasonTimeoutParent)
	}
}

func TestClassifyEmbedError_GenericError(t *testing.T) {
	got := classifyEmbedError(errors.New("boom"), context.Background())
	if got != engine.RelevanceReasonEmbedError {
		t.Fatalf("classifyEmbedError(generic) = %q, want %q", got, engine.RelevanceReasonEmbedError)
	}
}

// === #452: whose deadline expired — the gate's own budget vs the parent's ===

// TestClassifyEmbedError_GateBudgetTimeout exercises the branch where the
// GATE's own jobSearchRelevanceTimeout expired: the gate context was created
// with WithTimeoutCause(..., errGateBudget), so context.Cause returns
// errGateBudget → timeout_gate.
//
// Falsification F1: revert the gate's context creation to context.WithTimeout
// (drop the cause). context.Cause then returns context.DeadlineExceeded (the
// default), not errGateBudget → this test REDs (classifies timeout_parent).
func TestClassifyEmbedError_GateBudgetTimeout(t *testing.T) {
	gateCtx, cancel := context.WithTimeoutCause(context.Background(), time.Nanosecond, errGateBudget)
	defer cancel()
	// Wait for the gate's own deadline to fire so Cause is populated.
	<-gateCtx.Done()
	if !errors.Is(context.Cause(gateCtx), errGateBudget) {
		t.Fatalf("setup: gateCtx Cause must be errGateBudget after the gate budget expired, got %v", context.Cause(gateCtx))
	}
	got := classifyEmbedError(context.DeadlineExceeded, gateCtx)
	if got != engine.RelevanceReasonTimeoutGate {
		t.Fatalf("classifyEmbedError(DeadlineExceeded, gate-budget-expired) = %q, want %q (the gate's own budget expired)", got, engine.RelevanceReasonTimeoutGate)
	}
}

// TestClassifyEmbedError_ParentBudgetTimeout exercises the branch where the
// PARENT's deadline expired first: the gate context was created with a LONG
// own budget, but its parent expired, so context.Cause(gateCtx) returns the
// parent's cause (context.DeadlineExceeded), NOT errGateBudget → timeout_parent.
//
// Falsification F2: make the classifier always return timeout_gate (drop the
// parent branch). This test REDs (classifies timeout_gate instead of
// timeout_parent). The gate-budget test above stays GREEN under that mutation
// and REDs under the opposite mutation (always timeout_parent) — proving the
// two tests reach genuinely different classifications.
func TestClassifyEmbedError_ParentBudgetTimeout(t *testing.T) {
	// Parent with a short plain deadline (no cause → Cause == DeadlineExceeded).
	parent, parentCancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer parentCancel()
	// Gate with a long own budget; min(d, parent's remaining) = parent's.
	gateCtx, gateCancel := context.WithTimeoutCause(parent, 5*time.Second, errGateBudget)
	defer gateCancel()
	<-gateCtx.Done()
	if errors.Is(context.Cause(gateCtx), errGateBudget) {
		t.Fatalf("setup: gateCtx Cause must NOT be errGateBudget when the parent expired first, got errGateBudget — the parent's deadline did not fire before the gate's")
	}
	got := classifyEmbedError(context.DeadlineExceeded, gateCtx)
	if got != engine.RelevanceReasonTimeoutParent {
		t.Fatalf("classifyEmbedError(DeadlineExceeded, parent-budget-expired) = %q, want %q (the parent's deadline expired first)", got, engine.RelevanceReasonTimeoutParent)
	}
}

// === #452 Change 2: go-kit v0.97.11 retry.RetryError preserves the causal error ===

// TestClassifyEmbedError_RetryErrorPreservesCircuitOpen verifies that a
// *retry.RetryError — the shape go-kit v0.97.11's retry ladder returns when
// the gate context expires during the retry backoff after a circuit-open
// attempt — classifies as circuit_open, NOT timeout. Before v0.97.11 the
// ladder wrapped ctx.Err() with %w and the attempt error with %v, so
// errors.Is(err, ErrCircuitOpen) could not reach the causal error and the
// case was misclassified as timeout.
//
// Falsification F3: swap the classifier order so the deadline arm is checked
// BEFORE the circuit_open arm. errors.Is(retryErr, DeadlineExceeded) is true
// (RetryError.Is matches the ctxErr arm) → classifies as timeout_parent →
// this test REDs. The circuit_open arm being first is what makes the go-kit
// bump effective for this case.
func TestClassifyEmbedError_RetryErrorPreservesCircuitOpen(t *testing.T) {
	// Construct the v0.97.11 retry-ladder shape: an expired context whose
	// last attempt error was ErrCircuitOpen. retry.WrapContextErr returns a
	// *RetryError whose Is matches both ctxErr and the attempt error.
	expiredCtx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-expiredCtx.Done()
	retryErr := retry.WrapContextErr(expiredCtx, 3, kitembed.ErrCircuitOpen)

	// The error must match BOTH sentinels — the property v0.97.11 adds.
	if !errors.Is(retryErr, context.DeadlineExceeded) {
		t.Fatalf("setup: retry.WrapContextErr must wrap DeadlineExceeded, errors.Is = false")
	}
	if !errors.Is(retryErr, kitembed.ErrCircuitOpen) {
		t.Fatalf("setup: retry.WrapContextErr must preserve ErrCircuitOpen (go-kit v0.97.11 RetryError.Is), errors.Is = false — did the go-kit bump not take effect?")
	}

	got := classifyEmbedError(retryErr, context.Background())
	if got != engine.RelevanceReasonCircuitOpen {
		t.Fatalf("classifyEmbedError(RetryError{DeadlineExceeded, ErrCircuitOpen}) = %q, want %q — the causal error (circuit_open) must win over the context sentinel", got, engine.RelevanceReasonCircuitOpen)
	}
}

// TestRelevanceGate_DegradesOnCircuitOpen verifies the circuit_open embedder
// error triggers the fail-open path with the bounded reason label.
func TestRelevanceGate_DegradesOnCircuitOpen(t *testing.T) {
	withRelevanceEmbedder(t, relevanceCircuitOpenEmbedder{})
	withRelevanceConfig(t, 0.5, 1)

	results := []engine.SearxngResult{
		{Title: "Job A", URL: "http://example.com/a", Content: "some job"},
	}
	got, degraded, _ := applyRelevanceGate(context.Background(), "q", results)
	if len(got) != 1 {
		t.Fatalf("circuit_open must fail-open (return all), got %d", len(got))
	}
	if degraded != engine.RelevanceReasonCircuitOpen {
		t.Fatalf("expected degraded %q, got %q", engine.RelevanceReasonCircuitOpen, degraded)
	}
}

// TestRelevanceGate_DegradesOnGateBudgetTimeout verifies the timeout embedder
// error triggers the fail-open path with the timeout_gate reason label: the
// gate's OWN budget expired (parent = context.Background, no parent deadline),
// so context.Cause(gateCtx) == errGateBudget. Uses a shrunk gate timeout so
// the test returns promptly.
//
// Falsification F1: revert the gate's context creation to context.WithTimeout
// (drop the cause). Cause becomes DeadlineExceeded, not errGateBudget →
// classifies timeout_parent → this test REDs.
func TestRelevanceGate_DegradesOnGateBudgetTimeout(t *testing.T) {
	withRelevanceEmbedder(t, relevanceTimeoutEmbedder{})
	withRelevanceConfig(t, 0.5, 1)
	origTimeout := jobSearchRelevanceTimeout
	jobSearchRelevanceTimeout = 100 // 100ns — fires immediately
	t.Cleanup(func() { jobSearchRelevanceTimeout = origTimeout })

	results := []engine.SearxngResult{
		{Title: "Job A", URL: "http://example.com/a", Content: "some job"},
	}
	got, degraded, _ := applyRelevanceGate(context.Background(), "q", results)
	if len(got) != 1 {
		t.Fatalf("timeout must fail-open (return all), got %d", len(got))
	}
	if degraded != engine.RelevanceReasonTimeoutGate {
		t.Fatalf("expected degraded %q (gate's own budget expired; parent had no deadline), got %q", engine.RelevanceReasonTimeoutGate, degraded)
	}
}

// TestRelevanceGate_DegradesOnParentBudgetTimeout verifies the complementary
// branch: the PARENT's deadline expires first (parent = short deadline, gate
// timeout = long), so context.Cause(gateCtx) is the parent's cause, NOT
// errGateBudget → timeout_parent. The relevanceTimeoutEmbedder blocks on
// ctx.Done, so whichever context expires first drives the classification.
//
// Falsification F2: make the classifier always return timeout_gate (drop the
// parent branch). This test REDs (classifies timeout_gate). The gate-budget
// test above REDs under the opposite mutation (always timeout_parent) — the
// two tests reach genuinely different classifications.
func TestRelevanceGate_DegradesOnParentBudgetTimeout(t *testing.T) {
	withRelevanceEmbedder(t, relevanceTimeoutEmbedder{})
	withRelevanceConfig(t, 0.5, 1)
	// Gate timeout LONGER than the parent so the parent fires first.
	origTimeout := jobSearchRelevanceTimeout
	jobSearchRelevanceTimeout = 5 * time.Second
	t.Cleanup(func() { jobSearchRelevanceTimeout = origTimeout })

	parent, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	results := []engine.SearxngResult{
		{Title: "Job A", URL: "http://example.com/a", Content: "some job"},
	}
	got, degraded, _ := applyRelevanceGate(parent, "q", results)
	if len(got) != 1 {
		t.Fatalf("timeout must fail-open (return all), got %d", len(got))
	}
	if degraded != engine.RelevanceReasonTimeoutParent {
		t.Fatalf("expected degraded %q (parent's deadline expired first; gate budget was 5s), got %q", engine.RelevanceReasonTimeoutParent, degraded)
	}
}

// === M5: a per-item empty vector inside a full-length response is degradation,
// not a zero score ===
//
// Without the per-item check, the empty EmbedVector stays nil, MathReranker
// scores it 0, and a real match is silently dropped as "irrelevant" with
// degraded="". The gate must detect it and fail open.
//
// Falsification N4: drop the per-item empty-vector check. This test goes RED —
// degraded is empty and the empty-vector item is dropped instead of returned.
func TestRelevanceGate_M5_PerItemEmptyVectorDegrades(t *testing.T) {
	withRelevanceEmbedder(t, &relevancePerItemEmptyEmbedder{emptyIndex: 1})
	withRelevanceConfig(t, 0.5, 1)

	results := []engine.SearxngResult{
		{Title: "Job A", URL: "http://example.com/a", Content: "some job"},
		{Title: "Job B", URL: "http://example.com/b", Content: "another job"},
	}
	got, degraded, _ := applyRelevanceGate(context.Background(), "q", results)
	if degraded != engine.RelevanceReasonEmptyVectors {
		t.Fatalf("M5: per-item empty vector must degrade (reason=%q), got degraded=%q", engine.RelevanceReasonEmptyVectors, degraded)
	}
	if len(got) != len(results) {
		t.Fatalf("M5: per-item empty vector must fail-open (return all %d unfiltered), got %d — the empty-vector item was silently dropped as irrelevant", len(results), len(got))
	}
}

// TestRelevanceGate_PartialBatchDegrades verifies the len(vecs) < len(results)
// fail-open path (partial embeddings).
func TestRelevanceGate_PartialBatchDegrades(t *testing.T) {
	withRelevanceEmbedder(t, relevancePartialBatchEmbedder{})
	withRelevanceConfig(t, 0.5, 1)

	results := []engine.SearxngResult{
		{Title: "Job A", URL: "http://example.com/a", Content: "some job"},
		{Title: "Job B", URL: "http://example.com/b", Content: "another job"},
	}
	got, degraded, _ := applyRelevanceGate(context.Background(), "q", results)
	if degraded != engine.RelevanceReasonEmptyVectors {
		t.Fatalf("partial batch must degrade (reason=%q), got degraded=%q", engine.RelevanceReasonEmptyVectors, degraded)
	}
	if len(got) != len(results) {
		t.Fatalf("partial batch must fail-open (return all %d), got %d", len(results), len(got))
	}
}

// TestRelevanceGate_M3_CapTruncatesAndNotices verifies the candidate cap (M3):
// when more than maxRelevanceCandidates candidates are presented, the gate
// scores only the first cap and returns a non-empty notice (visible state, not
// silence). The gate still runs on the capped set (NOT fail-open: degraded="").
func TestRelevanceGate_M3_CapTruncatesAndNotices(t *testing.T) {
	withRelevanceEmbedder(t, &relevanceFakeEmbedder{queryVec: []float32{1, 0, 0}})
	withRelevanceConfig(t, 0.0, 0) // shipped defaults: keep all, no floor

	// Build maxRelevanceCandidates+5 results.
	results := make([]engine.SearxngResult, maxRelevanceCandidates+5)
	for i := range results {
		results[i] = engine.SearxngResult{Title: "scraping job", URL: "http://example.com/" + itoaTest(i), Content: "scraping"}
	}

	got, degraded, notice := applyRelevanceGate(context.Background(), "scraping", results)
	if degraded != "" {
		t.Fatalf("cap is NOT fail-open, expected degraded=\"\", got %q", degraded)
	}
	if notice == "" {
		t.Fatal("M3: cap trimmed candidates but notice is empty — truncation is silent")
	}
	if !stringContains(notice, "cap") {
		t.Fatalf("M3: notice must mention the cap, got %q", notice)
	}
	if len(got) != maxRelevanceCandidates {
		t.Fatalf("M3: expected %d scored results (cap), got %d", maxRelevanceCandidates, len(got))
	}
}

// === M2: the positional URL fallback must not mis-assign when Jobs is a
// filtered subsequence of top ===
//
// The LLM prompt rule tells the model to DROP non-matching listings, so
// jobOut.Jobs can be shorter than top. The unconditional fallback
// (j.URL = top[i].URL) would then graft the wrong URL onto each listing →
// wrong ExtractJobID → wrong liByJobID merge → corrupt persist. The guard
// applies the fallback ONLY when the two slices correspond (equal lengths).
//
// Falsification N3: restore the unconditional positional URL fallback (remove
// the len-equal guard). The "filtered subsequence" case below goes RED —
// jobs[0].URL gets mis-assigned "u1" instead of staying empty.
func TestAssignFallbackURLs_M2_NoMisassignWhenFiltered(t *testing.T) {
	// Filtered subsequence: 2 jobs, 3 top results → must NOT assign.
	jobs := []engine.JobListing{{}, {}}
	top := []engine.SearxngResult{{URL: "u1"}, {URL: "u2"}, {URL: "u3"}}
	assignFallbackURLs(jobs, top)
	if jobs[0].URL != "" || jobs[1].URL != "" {
		t.Fatalf("M2: must not mis-assign URLs when Jobs is a filtered subsequence (len 2 != len 3); got jobs[0].URL=%q jobs[1].URL=%q", jobs[0].URL, jobs[1].URL)
	}

	// Corresponding slices (equal length): assign positionally.
	jobs2 := []engine.JobListing{{URL: ""}, {URL: "already"}}
	top2 := []engine.SearxngResult{{URL: "u1"}, {URL: "u2"}}
	assignFallbackURLs(jobs2, top2)
	if jobs2[0].URL != "u1" {
		t.Fatalf("M2: must assign URL positionally when slices correspond; got jobs2[0].URL=%q want u1", jobs2[0].URL)
	}
	if jobs2[1].URL != "already" {
		t.Fatalf("M2: must not overwrite a non-empty URL; got jobs2[1].URL=%q want already", jobs2[1].URL)
	}
}

// itoaTest is a tiny int→string for test URL uniqueness (no strconv import
// needed in the test file).
func itoaTest(n int) string {
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

// === Fix C: the degradation notice is suppressed when the gate is inert, ===
// === kept when the gate is actually configured to filter              ===

// At shipped defaults (minRelevance=0, minKeep=0) the gate scores every
// candidate but filters none, so a degraded gate and a healthy gate produce
// IDENTICAL user-facing output. The "Relevance filtering unavailable" notice
// there distinguishes nothing and is alarming noise. It is kept when the
// gate is configured to filter, where the disclosure is real information. The
// floor/truncation notice is always shown. Both directions are tested — the
// second stops an over-broad fix from deleting the disclosure entirely.

func TestPrependRelevanceNotice_SuppressedWhenGateInert(t *testing.T) {
	withRelevanceConfig(t, 0.0, 0) // shipped defaults — gate inert

	got := prependRelevanceNotice("base summary", "timeout", "")
	if stringContains(got, "Relevance filtering unavailable") {
		t.Fatalf("degradation notice must be suppressed when the gate is inert (it distinguishes nothing), got %q", got)
	}
	// The floor/truncation notice is independent of the degraded notice and
	// must still be shown when the gate is inert — it describes a real state.
	got = prependRelevanceNotice("base summary", "timeout", "relevance floor engaged: 1 result(s) kept below the bar")
	if !stringContains(got, "floor") {
		t.Fatalf("floor notice must still be shown when the gate is inert, got %q", got)
	}
}

func TestPrependRelevanceNotice_ShownWhenGateFilters(t *testing.T) {
	withRelevanceConfig(t, 0.5, 1) // configured to filter — disclosure is real

	got := prependRelevanceNotice("base summary", "timeout", "")
	if !stringContains(got, "Relevance filtering unavailable") {
		t.Fatalf("degradation notice must be shown when the gate is configured to filter, got %q", got)
	}
}
