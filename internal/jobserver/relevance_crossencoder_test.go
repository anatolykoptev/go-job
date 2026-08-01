package jobserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/anatolykoptev/go-kit/rerank"
	"github.com/anatolykoptev/go_job/internal/engine"
)

// withRelevanceReranker installs a rerank client pointing at an httptest server
// for the duration of the test and restores the previous value on cleanup.
// The server's handler controls the cross-encoder scores returned.
func withRelevanceReranker(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	prev := relevanceRerankClient
	prevTimeout := jobSearchCrossEncoderTimeout
	jobSearchCrossEncoderTimeout = 5 * time.Second
	t.Cleanup(func() {
		relevanceRerankClient = prev
		jobSearchCrossEncoderTimeout = prevTimeout
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	relevanceRerankClient = rerank.NewClient(srv.URL,
		rerank.WithModel(crossEncoderModel),
		rerank.WithTimeout(jobSearchCrossEncoderTimeout),
		rerank.WithRetry(rerank.NoRetry),
	)
}

// withRelevanceRerankerNil installs a nil rerank client (shadow not configured).
func withRelevanceRerankerNil(t *testing.T) {
	t.Helper()
	prev := relevanceRerankClient
	relevanceRerankClient = nil
	t.Cleanup(func() { relevanceRerankClient = prev })
}

// rerankRequest is the Cohere-style /v1/rerank request body.
type rerankRequest struct {
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
}

// rerankResponse is the Cohere-style /v1/rerank response body.
type rerankResponse struct {
	Results []rerankResult `json:"results"`
}

type rerankResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

// flipRerankHandler returns cross-encoder scores that FLIP the cosine decision:
// on-topic docs (containing scraping markers) get a LOW score (0.2, below the
// 0.5 midpoint → cross-encoder would reject); off-topic docs get a HIGH score
// (0.8, above 0.5 → cross-encoder would keep). This is the adversarial input
// for the shadow-invariant test: if ANY cross-encoder score reaches the
// decision, the returned set changes.
func flipRerankHandler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req rerankRequest
	_ = json.Unmarshal(body, &req)
	resp := rerankResponse{Results: make([]rerankResult, len(req.Documents))}
	for i, doc := range req.Documents {
		if containsScraping(doc) {
			resp.Results[i] = rerankResult{Index: i, RelevanceScore: 0.2} // on-topic → low (flip)
		} else {
			resp.Results[i] = rerankResult{Index: i, RelevanceScore: 0.8} // off-topic → high (flip)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// agreeRerankHandler returns cross-encoder scores that AGREE with the cosine
// decision: on-topic docs get HIGH (0.8), off-topic get LOW (0.2).
func agreeRerankHandler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req rerankRequest
	_ = json.Unmarshal(body, &req)
	resp := rerankResponse{Results: make([]rerankResult, len(req.Documents))}
	for i, doc := range req.Documents {
		if containsScraping(doc) {
			resp.Results[i] = rerankResult{Index: i, RelevanceScore: 0.8}
		} else {
			resp.Results[i] = rerankResult{Index: i, RelevanceScore: 0.2}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// errorRerankHandler returns HTTP 500 — the cross-encoder error path.
func errorRerankHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusInternalServerError)
}

// slowRerankHandler sleeps for 6s — longer than the 5s client timeout. The
// client cancels after 5s and returns a deadline-exceeded error; the handler
// returns after 6s total. srv.Close() in t.Cleanup waits ~1s for the handler
// to finish. This avoids the httptest.Server.Close deadlock that a
// r.Context().Done() block creates (Close waits for the handler, the handler
// waits for r.Context(), r.Context() waits for Close — circular).
func slowRerankHandler(w http.ResponseWriter, _ *http.Request) {
	time.Sleep(6 * time.Second)
	w.WriteHeader(http.StatusOK)
}

// agreementDeltas returns how much each agreement outcome counter moved across fn.
func agreementDeltas(fn func()) map[string]int64 {
	key := func(outcome string) string {
		return engine.MetricJobSearchRelevanceAgreement + "{outcome=" + outcome + "}"
	}
	before := engine.GetMetrics()
	fn()
	after := engine.GetMetrics()
	out := map[string]int64{}
	for _, oc := range []string{
		engine.AgreeKept, engine.AgreeRejected,
		engine.DisagreeCosineKeptXEReject, engine.DisagreeCosineRejXEKeeps,
	} {
		out[oc] = after[key(oc)] - before[key(oc)]
	}
	return out
}

// crossEncoderDegradedDelta returns how much the cross-encoder degraded counter
// moved across fn (summed over all reasons).
func crossEncoderDegradedDelta(fn func()) int64 {
	before := engine.GetMetrics()
	fn()
	after := engine.GetMetrics()
	var delta int64
	for _, r := range []string{
		engine.CrossEncoderReasonNotConfigured, engine.CrossEncoderReasonTimeout,
		engine.CrossEncoderReasonError, engine.CrossEncoderReasonCircuitOpen,
		engine.CrossEncoderReasonEmpty,
	} {
		key := engine.MetricJobSearchCrossEncoderDegraded + "{reason=" + r + "}"
		delta += after[key] - before[key]
	}
	return delta
}

// resultURLs returns the URLs of the gate's output in order.
func resultURLs(rs []engine.SearxngResult) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.URL
	}
	return out
}

// T1 — SHADOW INVARIANT: with the cross-encoder returning scores that would
// flip every decision, the set of returned listings is unchanged (identical to
// the no-cross-encoder build).
//
// Falsification: let the cross-encoder score reach the decision. Mutate
// observeCrossEncoderShadow to overwrite the cosine score:
//   engine.ObserveJobSearchCrossEncoderScore(xeScore)  →  r.Score = xeScore
// AND move the observeCrossEncoderShadow call before
//   filtered := engine.FilterByScore(sorted, jobSearchMinRelevance, jobSearchMinKeep)
// The filter then runs on cross-encoder scores → the flip run returns off-topic
// listings (cross-encoder keeps them) while the nil run returns on-topic → RED.
func TestCrossEncoderShadow_T1_Invariant_ReturnsUnchanged(t *testing.T) {
	engine.InitTestRegistry()

	results := []engine.SearxngResult{
		{Title: "Web Scraping Engineer", URL: "http://example.com/1", Content: "anti-bot browser automation"},
		{Title: "Web Developer", URL: "http://example.com/2", Content: "frontend web development"},
		{Title: "Senior Automation Engineer", URL: "http://example.com/3", Content: "browser automation testing"},
		{Title: "Frontend Engineer", URL: "http://example.com/4", Content: "react frontend"},
	}

	// Reference run: no cross-encoder (nil). Cosine decision: on-topic kept.
	withRelevanceEmbedder(t, &relevanceNarrowEmbedder{queryVec: []float32{1, 0}})
	withRelevanceRerankerNil(t)
	withRelevanceConfig(t, 0.845, 0)
	refGot, refDegraded, _ := applyRelevanceGate(context.Background(), "web scraping anti-bot", results)
	if refDegraded != "" {
		t.Fatalf("reference run degraded: %q", refDegraded)
	}

	// Shadow run: flip cross-encoder. The returned set MUST be identical.
	withRelevanceEmbedder(t, &relevanceNarrowEmbedder{queryVec: []float32{1, 0}})
	withRelevanceReranker(t, flipRerankHandler)
	withRelevanceConfig(t, 0.845, 0)
	shadowGot, shadowDegraded, _ := applyRelevanceGate(context.Background(), "web scraping anti-bot", results)
	if shadowDegraded != "" {
		t.Fatalf("shadow run gate degraded (cross-encoder must be invisible): %q", shadowDegraded)
	}

	if len(refGot) != len(shadowGot) {
		t.Fatalf("T1: shadow changed the returned set size: ref=%d shadow=%d (ref=%v shadow=%v)",
			len(refGot), len(shadowGot), resultURLs(refGot), resultURLs(shadowGot))
	}
	for i := range refGot {
		if refGot[i].URL != shadowGot[i].URL {
			t.Fatalf("T1: shadow changed the returned set: ref[%d]=%s shadow[%d]=%s\nref=%v\nshadow=%v",
				i, refGot[i].URL, i, shadowGot[i].URL, resultURLs(refGot), resultURLs(shadowGot))
		}
		// The cosine Score must be preserved (the shadow must not overwrite it).
		if refGot[i].Score != shadowGot[i].Score {
			t.Fatalf("T1: shadow overwrote cosine score for %s: ref=%.4f shadow=%.4f",
				refGot[i].URL, refGot[i].Score, shadowGot[i].Score)
		}
	}
}

// T2 — CROSS-ENCODER FAILURE: timeout / non-200 / unconfigured leaves the
// returned listings identical to the no-cross-encoder build, and increments the
// degraded counter.
//
// Falsification: remove the degraded counter bump in observeCrossEncoderShadow:
//   engine.IncrJobSearchCrossEncoderDegraded(classifyCrossEncoderError(err))  →  (deleted)
// The degraded delta is 0 → RED.
func TestCrossEncoderShadow_T2_FailureInvisibleAndDegraded(t *testing.T) {
	engine.InitTestRegistry()

	results := []engine.SearxngResult{
		{Title: "Web Scraping Engineer", URL: "http://example.com/1", Content: "anti-bot browser automation"},
		{Title: "Web Developer", URL: "http://example.com/2", Content: "frontend web development"},
	}

	// Reference: nil cross-encoder.
	withRelevanceEmbedder(t, &relevanceNarrowEmbedder{queryVec: []float32{1, 0}})
	withRelevanceRerankerNil(t)
	withRelevanceConfig(t, 0.845, 0)
	refGot, _, _ := applyRelevanceGate(context.Background(), "web scraping anti-bot", results)
	refURLs := resultURLs(refGot)

	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"non-200", errorRerankHandler},
		{"timeout", slowRerankHandler},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withRelevanceEmbedder(t, &relevanceNarrowEmbedder{queryVec: []float32{1, 0}})
			withRelevanceReranker(t, tc.handler)
			withRelevanceConfig(t, 0.845, 0)

			var got []engine.SearxngResult
			var degraded string
			d := crossEncoderDegradedDelta(func() {
				got, degraded, _ = applyRelevanceGate(context.Background(), "web scraping anti-bot", results)
			})
			if degraded != "" {
				t.Fatalf("T2: cross-encoder failure must be invisible to the gate (degraded=%q)", degraded)
			}
			if d <= 0 {
				t.Fatalf("T2: cross-encoder failure must increment the degraded counter, got delta=%d", d)
			}
			gotURLs := resultURLs(got)
			if len(gotURLs) != len(refURLs) {
				t.Fatalf("T2: failure changed the returned set size: ref=%d got=%d", len(refURLs), len(gotURLs))
			}
			for i := range refURLs {
				if refURLs[i] != gotURLs[i] {
					t.Fatalf("T2: failure changed the returned set: ref[%d]=%s got[%d]=%s", i, refURLs[i], i, gotURLs[i])
				}
			}
		})
	}

	// Unconfigured: nil rerank client. The gate scored (embed configured), so
	// the shadow bumps not_configured.
	t.Run("unconfigured", func(t *testing.T) {
		withRelevanceEmbedder(t, &relevanceNarrowEmbedder{queryVec: []float32{1, 0}})
		withRelevanceRerankerNil(t)
		withRelevanceConfig(t, 0.845, 0)

		var got []engine.SearxngResult
		var degraded string
		d := crossEncoderDegradedDelta(func() {
			got, degraded, _ = applyRelevanceGate(context.Background(), "web scraping anti-bot", results)
		})
		if degraded != "" {
			t.Fatalf("T2: unconfigured cross-encoder must be invisible to the gate (degraded=%q)", degraded)
		}
		if d <= 0 {
			t.Fatalf("T2: unconfigured cross-encoder must increment the degraded counter, got delta=%d", d)
		}
		gotURLs := resultURLs(got)
		if len(gotURLs) != len(refURLs) {
			t.Fatalf("T2: unconfigured changed the returned set size: ref=%d got=%d", len(refURLs), len(gotURLs))
		}
		for i := range refURLs {
			if refURLs[i] != gotURLs[i] {
				t.Fatalf("T2: unconfigured changed the returned set: ref[%d]=%s got[%d]=%s", i, refURLs[i], i, gotURLs[i])
			}
		}
	})
}

// T3 — AGREEMENT COUNTER DISTINGUISHES BOTH DIRECTIONS: with the flip
// cross-encoder, on-topic candidates are cosine-kept but cross-encoder-rejected
// (disagree_cosine_kept_xe_rejects) and off-topic candidates are cosine-rejected
// but cross-encoder-kept (disagree_cosine_rejected_xe_keeps). Both labels must
// be incremented distinctly.
//
// Falsification: collapse both disagreement directions into one label. Mutate
// agreementOutcome so the two disagree branches return the same label:
//   case cosineKept && !xeWouldKeep: return engine.DisagreeCosineKeptXEReject
//   default:                          return engine.DisagreeCosineRejXEKeeps
// → change the default to also return engine.DisagreeCosineKeptXEReject.
// Then disagree_cosine_rejected_xe_keeps delta is 0 → RED.
func TestCrossEncoderShadow_T3_AgreementDistinguishesDirections(t *testing.T) {
	engine.InitTestRegistry()

	results := []engine.SearxngResult{
		{Title: "Web Scraping Engineer", URL: "http://example.com/1", Content: "anti-bot browser automation"},  // on-topic
		{Title: "Web Developer", URL: "http://example.com/2", Content: "frontend web development"},             // off-topic
		{Title: "Senior Automation Engineer", URL: "http://example.com/3", Content: "browser automation testing"}, // on-topic
		{Title: "Frontend Engineer", URL: "http://example.com/4", Content: "react frontend"},                   // off-topic
	}

	withRelevanceEmbedder(t, &relevanceNarrowEmbedder{queryVec: []float32{1, 0}})
	withRelevanceReranker(t, flipRerankHandler)
	withRelevanceConfig(t, 0.845, 0)

	var degraded string
	d := agreementDeltas(func() {
		_, degraded, _ = applyRelevanceGate(context.Background(), "web scraping anti-bot", results)
	})
	if degraded != "" {
		t.Fatalf("gate degraded: %q", degraded)
	}

	// On-topic (cosine-kept, xe-rejected): 2 candidates.
	if d[engine.DisagreeCosineKeptXEReject] != 2 {
		t.Fatalf("T3: disagree_cosine_kept_xe_rejects delta=%d, want 2 (on-topic candidates cosine-kept but xe-rejected)",
			d[engine.DisagreeCosineKeptXEReject])
	}
	// Off-topic (cosine-rejected, xe-kept): 2 candidates.
	if d[engine.DisagreeCosineRejXEKeeps] != 2 {
		t.Fatalf("T3: disagree_cosine_rejected_xe_keeps delta=%d, want 2 (off-topic candidates cosine-rejected but xe-kept)",
			d[engine.DisagreeCosineRejXEKeeps])
	}
	// Both disagreement directions must be DISTINCT labels — one must not absorb
	// the other. If a mutation collapses them, one delta is 0 and the other is 4.
	keptXEReject := d[engine.DisagreeCosineKeptXEReject]
	rejXEKeeps := d[engine.DisagreeCosineRejXEKeeps]
	if keptXEReject == 4 && rejXEKeeps == 4 {
		t.Fatalf("T3: both disagreement directions collapsed into one label (each should be 2, got %d and %d) — the directions are not distinguishable",
			keptXEReject, rejXEKeeps)
	}
}

// T4 — SCORE HISTOGRAM + CANDIDATE-SET HISTOGRAM: the cross-encoder score
// histogram records one observation per scored candidate, and the candidate-set
// size histogram records one observation per gate application.
//
// Falsification: remove the ObserveJobSearchCrossEncoderScore call in
// observeCrossEncoderShadow:
//   engine.ObserveJobSearchCrossEncoderScore(xeScore)  →  (deleted)
// The histogram Count is 0 → RED.
func TestCrossEncoderShadow_T4_HistogramsRecorded(t *testing.T) {
	engine.InitTestRegistry()

	results := []engine.SearxngResult{
		{Title: "Web Scraping Engineer", URL: "http://example.com/1", Content: "anti-bot browser automation"},
		{Title: "Web Developer", URL: "http://example.com/2", Content: "frontend web development"},
		{Title: "Senior Automation Engineer", URL: "http://example.com/3", Content: "browser automation testing"},
	}

	withRelevanceEmbedder(t, &relevanceNarrowEmbedder{queryVec: []float32{1, 0}})
	withRelevanceReranker(t, agreeRerankHandler)
	withRelevanceConfig(t, 0.0, 0) // keep all so all 3 are scored + shadow-scored

	got, degraded, _ := applyRelevanceGate(context.Background(), "web scraping anti-bot", results)
	if degraded != "" {
		t.Fatalf("gate degraded: %q", degraded)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 results, got %d", len(got))
	}

	scoreSnap := engine.GetHistogramSnapshot(engine.MetricJobSearchCrossEncoderScore)
	if scoreSnap.Count != 3 {
		t.Fatalf("T4: cross-encoder score histogram Count=%d, want 3 (one per scored candidate)", scoreSnap.Count)
	}

	setSnap := engine.GetHistogramSnapshot(engine.MetricJobSearchCandidateSetSize)
	if setSnap.Count != 1 {
		t.Fatalf("T4: candidate-set size histogram Count=%d, want 1 (one per gate application)", setSnap.Count)
	}
}

// T5 — CONTROL: an existing relevance gate test stays green with the shadow
// wired, proving the shadow did not break the gate's cosine decision. This is
// the unrelated-test control required by the falsification protocol.
func TestCrossEncoderShadow_T5_Control_ExistingGateStillGreen(t *testing.T) {
	withRelevanceEmbedder(t, &relevanceFakeEmbedder{queryVec: []float32{1, 0, 0}})
	withRelevanceRerankerNil(t) // shadow not configured — gate behaves as before
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

// containsScraping and stringContains are reused from relevance_gate_test.go
// (same package).
