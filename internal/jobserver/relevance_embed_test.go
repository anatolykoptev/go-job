package jobserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	kitembed "github.com/anatolykoptev/go-kit/embed"
	"github.com/anatolykoptev/go_job/internal/engine"
)

// === Fix A: the embed envelope must fit strictly inside the gate budget ===

// TestRelevanceEmbedBudget_InvariantFitsGateTimeout asserts the invariant
// worstCaseEmbedEnvelope() < jobSearchRelevanceTimeout for the configured
// budget, then mutates perRequest up to the gate budget to prove the assertion
// is not vacuous (the pre-fix 30s-default inversion would make the envelope
// 2× the budget).
//
// Falsification: set the per-request timeout to the gate budget (or above).
// The envelope can no longer fit → this test goes RED.
func TestRelevanceEmbedBudget_InvariantFitsGateTimeout(t *testing.T) {
	if worstCaseEmbedEnvelope() >= jobSearchRelevanceTimeout {
		t.Fatalf("embed envelope worst case (%v) must be strictly less than the gate budget (%v)",
			worstCaseEmbedEnvelope(), jobSearchRelevanceTimeout)
	}
	// Mutation: per-request == gate budget (the pre-fix inversion). The
	// envelope must now NOT fit — this is the RED that proves the green
	// above is not vacuous.
	orig := relevanceEmbedPerRequest
	relevanceEmbedPerRequest = jobSearchRelevanceTimeout
	t.Cleanup(func() { relevanceEmbedPerRequest = orig })
	if worstCaseEmbedEnvelope() < jobSearchRelevanceTimeout {
		t.Fatalf("mutation: per-request == gate budget must make the envelope NOT fit (got %v < %v) — the invariant assertion is vacuous",
			worstCaseEmbedEnvelope(), jobSearchRelevanceTimeout)
	}
}

// === Fix A: a transient retryable failure is absorbed, not surfaced as a deadline ===

// retryReachabilityEmbedder simulates the real embed client's retry behaviour
// inside the gate's single context: the first EmbedQuery attempt is a slow
// request that the per-request HTTP timeout cuts (→ a retryable failure), and
// the retry succeeds. The simulation is gated on the configured per-request
// budget: if perRequest < the gate budget, the cut happens before the gate
// context fires and the retry succeeds; if perRequest >= the gate budget (the
// 30s-default mutation), the gate context fires first and the gate sees a
// deadline instead of the eventual success — the defect.
type retryReachabilityEmbedder struct {
	perRequest time.Duration
}

func (e *retryReachabilityEmbedder) EmbedQuery(ctx context.Context, _ string) ([]float32, error) {
	select {
	case <-time.After(e.perRequest):
		// per-request timeout cut the slow first attempt → retry succeeds.
		return []float32{1, 0, 0}, nil
	case <-ctx.Done():
		// gate context fired before the per-request cut → deadline (the defect).
		return nil, ctx.Err()
	}
}

func (e *retryReachabilityEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1, 0, 0}
	}
	return out, nil
}

func (e *retryReachabilityEmbedder) Dimension() int { return 3 }
func (e *retryReachabilityEmbedder) Close() error   { return nil }

// TestRelevanceEmbedBudget_RetryReachesSuccess asserts that with the derived
// per-request budget the gate OBSERVES the eventual success rather than a
// deadline when the embedder fails retryably and then succeeds.
//
// Falsification: restore the 30s per-request default (make computeEmbedPerRequest
// return 30s, or set the fake's perRequest to 30s). The gate context fires
// before the per-request cut → the gate degrades with reason=timeout → RED.
func TestRelevanceEmbedBudget_RetryReachesSuccess(t *testing.T) {
	// Shrunk gate timeout for a fast test; large enough that the derived
	// per-request is positive.
	origTimeout := jobSearchRelevanceTimeout
	jobSearchRelevanceTimeout = 2 * time.Second
	t.Cleanup(func() { jobSearchRelevanceTimeout = origTimeout })

	perRequest := computeEmbedPerRequest(jobSearchRelevanceTimeout)
	if perRequest <= 0 {
		t.Fatalf("derived per-request must be positive for a 2s gate, got %v", perRequest)
	}

	withRelevanceEmbedder(t, &retryReachabilityEmbedder{perRequest: perRequest})
	withRelevanceConfig(t, 0.0, 0)

	results := []engine.SearxngResult{
		{Title: "Web Scraping Engineer", URL: "http://example.com/a", Content: "scraping"},
	}
	got, degraded, _ := applyRelevanceGate(context.Background(), "scraping", results)
	if degraded != "" {
		t.Fatalf("retry must absorb the transient slow attempt so the gate succeeds, got degraded=%q", degraded)
	}
	if len(got) != 1 {
		t.Fatalf("expected the 1 result through the gate, got %d", len(got))
	}
}

// === Fix B: the candidate cap yields exactly one upstream chunk ===

// embedTestServer is an httptest server speaking the OpenAI /v1/embeddings
// shape. It counts POSTs and records the max input length so the chunking
// test can assert the candidate set is a single round-trip.
type embedTestServer struct {
	*httptest.Server
	requests    int32
	maxInputLen int32
}

func newEmbedTestServer(t *testing.T, dim int) *embedTestServer {
	t.Helper()
	s := &embedTestServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.Unmarshal(body, &req)
		atomic.AddInt32(&s.requests, 1)
		if int32(len(req.Input)) > atomic.LoadInt32(&s.maxInputLen) {
			atomic.StoreInt32(&s.maxInputLen, int32(len(req.Input)))
		}
		vec := make([]float32, dim)
		if dim > 0 {
			vec[0] = 1
		}
		type item struct {
			Object    string    `json:"object"`
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		}
		data := make([]item, len(req.Input))
		for i := range data {
			data[i] = item{Object: "embedding", Embedding: vec, Index: i}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data":   data,
			"model":  "test",
		})
	}))
	t.Cleanup(s.Close)
	return s
}

// TestRelevanceEmbedBudget_CandidateCapIsOneChunk asserts the cap equals the
// chunk size (the relationship, in code) AND that a full cap of candidates
// drives the REAL kitembed client to exactly one upstream passage request.
//
// Falsification: raise maxRelevanceCandidates above the chunk size (e.g. set
// the const to 33). The cap > chunk size → the passage call splits into two
// chunks → two passage requests → this test goes RED.
func TestRelevanceEmbedBudget_CandidateCapIsOneChunk(t *testing.T) {
	if maxRelevanceCandidates > relevanceEmbedChunkSize {
		t.Fatalf("candidate cap (%d) must not exceed the chunk size (%d) so the candidate set is one round-trip",
			maxRelevanceCandidates, relevanceEmbedChunkSize)
	}

	srv := newEmbedTestServer(t, 3)
	client, err := NewEmbedClient(srv.URL,
		kitembed.WithBackend("http"),
		kitembed.WithDim(3),
	)
	if err != nil {
		t.Fatalf("NewEmbedClient: %v", err)
	}
	withRelevanceEmbedder(t, client)
	withRelevanceConfig(t, 0.0, 0)

	results := make([]engine.SearxngResult, maxRelevanceCandidates)
	for i := range results {
		results[i] = engine.SearxngResult{
			Title:   "scraping job",
			URL:     "http://example.com/" + itoaTest(i),
			Content: "scraping",
		}
	}

	got, degraded, _ := applyRelevanceGate(context.Background(), "scraping", results)
	if degraded != "" {
		t.Fatalf("gate must succeed on a full cap of candidates, got degraded=%q", degraded)
	}
	if len(got) != maxRelevanceCandidates {
		t.Fatalf("expected %d results through the gate, got %d", maxRelevanceCandidates, len(got))
	}
	// EmbedQuery is 1 request; Embed(passages) must be exactly one chunk → 1
	// request. Total = 2. The single passage request must carry all candidates.
	totalRequests := atomic.LoadInt32(&srv.requests)
	if totalRequests != 2 {
		t.Fatalf("candidate cap must yield exactly one query + one passage chunk (2 requests), got %d", totalRequests)
	}
	if got := atomic.LoadInt32(&srv.maxInputLen); got != int32(maxRelevanceCandidates) {
		t.Fatalf("the single passage chunk must carry all %d candidates, got max input len %d", maxRelevanceCandidates, got)
	}
}

// === Reflection helpers: read a *kitembed.Client's constructed config ===
//
// kitembed.Client's retry/chunkSize fields and the inner HTTPEmbedder's
// *http.Client are unexported, so a jobserver test cannot call .Interface() on
// them. reflect permits .Int() on a field reached through unexported parents
// (only .Interface()/.Addr()/.Set() are denied for unexported fields), so we
// navigate the chain and read the numeric values directly. This is a
// structural wiring check — it asserts an Opt was actually applied to the
// constructed client, which a behavioural test against a bare fake cannot
// reach (retryReachabilityEmbedder never exercises NewEmbedClient).

// clientHTTPTimeout reads the per-request HTTP timeout the client was
// constructed with, by reflecting through Client.inner (a *HTTPEmbedder for
// the http backend) to its *http.Client.Timeout (an exported time.Duration).
func clientHTTPTimeout(c *kitembed.Client) time.Duration {
	v := reflect.ValueOf(c).Elem()             // *Client -> Client
	inner := v.FieldByName("inner")            // Embedder interface (unexported)
	concrete := inner.Elem()                   // *HTTPEmbedder (concrete value)
	httpEmb := concrete.Elem()                 // HTTPEmbedder struct
	clientVal := httpEmb.FieldByName("client") // *http.Client (unexported)
	httpClient := clientVal.Elem()             // http.Client struct
	return time.Duration(httpClient.FieldByName("Timeout").Int())
}

// clientRetryMaxAttempts reads the v2 RetryPolicy.MaxAttempts (3 =
// defaultRetryPolicy, 1 = NoRetry).
func clientRetryMaxAttempts(c *kitembed.Client) int {
	v := reflect.ValueOf(c).Elem()
	return int(v.FieldByName("retry").FieldByName("MaxAttempts").Int())
}

// clientChunkSize reads the client-side chunking limit.
func clientChunkSize(c *kitembed.Client) int {
	v := reflect.ValueOf(c).Elem()
	return int(v.FieldByName("chunkSize").Int())
}

// === F1: the gate's budgets must NOT leak onto the shared (non-gate) client ===

// TestRelevanceEmbedBudget_NonGateClientKeepsLibraryDefaults asserts the
// shared embed client — the one consumed by algora ingest, resume-vector sync,
// and profile sync (jobs.SetEmbedClient) — keeps kitembed's library defaults
// when constructed with ONLY the base opts (no EmbedClientBudgetOpts): the
// default retry policy (MaxAttempts=3) and the 30s per-request timeout.
//
// The gate's WithRetry(NoRetry) and WithTimeout(~1.84s) are correct for a 15s
// gate but fatal for a background ingest job that legitimately wants retries
// and a long timeout — one 503 during resume ingest would fail on the first
// attempt where it previously retried.
//
// Falsification: re-apply EmbedClientBudgetOpts() to the shared client's
// construction (the pre-fix wiring that bound the budgets to the singleton).
// MaxAttempts becomes 1 (NoRetry) and the timeout becomes
// relevanceEmbedPerRequest → both assertions RED.
func TestRelevanceEmbedBudget_NonGateClientKeepsLibraryDefaults(t *testing.T) {
	srv := newEmbedTestServer(t, 3)
	// The shared client is constructed with ONLY base opts — no budget opts.
	client, err := kitembed.NewClient(srv.URL,
		kitembed.WithBackend("http"),
		kitembed.WithDim(3),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if got := clientRetryMaxAttempts(client); got != 3 {
		t.Fatalf("non-gate client must keep the default retry policy (MaxAttempts=3), got %d — the gate's WithRetry(NoRetry) leaked onto the shared client", got)
	}
	if got := clientHTTPTimeout(client); got != 30*time.Second {
		t.Fatalf("non-gate client must keep the default 30s per-request timeout, got %v — the gate's WithTimeout(%v) leaked onto the shared client", got, relevanceEmbedPerRequest)
	}
}

// === F2: the gate client's per-request timeout must equal relevanceEmbedPerRequest ===

// TestRelevanceEmbedBudget_GateClientPerRequestTimeout asserts the gate's embed
// client (constructed via NewEmbedClient, which applies EmbedClientBudgetOpts)
// has its per-request HTTP timeout set to relevanceEmbedPerRequest — the core
// of fix A. The pre-fix code passed no WithTimeout, leaving kitembed's 30s
// default (2× the 15s gate budget), so the gate deadline was the only timeout
// that could fire and the retry policy could not complete inside the gate.
//
// It also asserts the gate's v2 retry is NoRetry (MaxAttempts=1, so it does
// not compound on the v1 HTTPEmbedder retry) and the chunk size equals
// relevanceEmbedChunkSize.
//
// Falsification: remove kitembed.WithTimeout(relevanceEmbedPerRequest) from
// EmbedClientBudgetOpts → the timeout stays at the 30s default → the timeout
// assertion RED.
func TestRelevanceEmbedBudget_GateClientPerRequestTimeout(t *testing.T) {
	srv := newEmbedTestServer(t, 3)
	client, err := NewEmbedClient(srv.URL,
		kitembed.WithBackend("http"),
		kitembed.WithDim(3),
	)
	if err != nil {
		t.Fatalf("NewEmbedClient: %v", err)
	}
	if got := clientHTTPTimeout(client); got != relevanceEmbedPerRequest {
		t.Fatalf("gate client per-request timeout must equal relevanceEmbedPerRequest (%v), got %v — WithTimeout was not applied (the 30s default inversion)", relevanceEmbedPerRequest, got)
	}
	if got := clientRetryMaxAttempts(client); got != 1 {
		t.Fatalf("gate client must disable v2 retry (NoRetry, MaxAttempts=1) so it does not compound on the v1 retry, got %d", got)
	}
	if got := clientChunkSize(client); got != relevanceEmbedChunkSize {
		t.Fatalf("gate client chunk size must equal relevanceEmbedChunkSize (%d), got %d", relevanceEmbedChunkSize, got)
	}
}
