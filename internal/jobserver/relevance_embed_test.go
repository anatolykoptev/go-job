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
// reach.

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

// === F1: the gate's opts must NOT leak onto the shared (non-gate) client ===

// TestRelevanceEmbedBudget_NonGateClientKeepsLibraryDefaults asserts the
// shared embed client — the one consumed by algora ingest, resume-vector sync,
// and profile sync (jobs.SetEmbedClient) — keeps kitembed's library defaults
// when constructed with ONLY the base opts (no EmbedClientBudgetOpts): the
// default retry policy (MaxAttempts=3) and the 30s per-request timeout.
//
// The gate's WithRetry(NoRetry) is correct for the gate but fatal for a
// background ingest job that legitimately wants retries — one 503 during
// resume ingest would fail on the first attempt where it previously retried.
//
// Falsification: re-apply EmbedClientBudgetOpts() to the shared client's
// construction (the pre-fix wiring that bound the opts to the singleton).
// MaxAttempts becomes 1 (NoRetry) → the retry assertion RED.
func TestRelevanceEmbedBudget_NonGateClientKeepsLibraryDefaults(t *testing.T) {
	srv := newEmbedTestServer(t, 3)
	// The shared client is constructed with ONLY base opts — no gate opts.
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
		t.Fatalf("non-gate client must keep the default 30s per-request timeout, got %v — a gate opt leaked onto the shared client", got)
	}
}

// === F2: the gate client must NOT carry a bespoke per-request timeout ===

// TestRelevanceEmbedBudget_NoBespokePerRequestTimeout asserts the gate's embed
// client (constructed via NewEmbedClient, which applies EmbedClientBudgetOpts)
// uses kitembed's library default per-request timeout (30s), NOT a bespoke
// derived value. The gate context (jobSearchRelevanceTimeout) is the sole
// outer bound; a per-request timeout tighter than the feature's own budget
// converts a graceful degradation into a hard failure (the prior 1.84s
// derived timeout fired on every gate call before the server responded).
//
// It also asserts the gate's v2 retry is NoRetry (MaxAttempts=1, so it does
// not compound on the v1 HTTPEmbedder retry) and the chunk size equals
// relevanceEmbedChunkSize.
//
// Falsification: add kitembed.WithTimeout(anything) to EmbedClientBudgetOpts.
// The gate client's timeout is no longer the 30s default → the timeout
// assertion RED.
func TestRelevanceEmbedBudget_NoBespokePerRequestTimeout(t *testing.T) {
	srv := newEmbedTestServer(t, 3)
	client, err := NewEmbedClient(srv.URL,
		kitembed.WithBackend("http"),
		kitembed.WithDim(3),
	)
	if err != nil {
		t.Fatalf("NewEmbedClient: %v", err)
	}
	if got := clientHTTPTimeout(client); got != 30*time.Second {
		t.Fatalf("gate client must use the library default 30s per-request timeout (no bespoke WithTimeout), got %v — EmbedClientBudgetOpts reintroduced a derived per-request timeout", got)
	}
	if got := clientRetryMaxAttempts(client); got != 1 {
		t.Fatalf("gate client must disable v2 retry (NoRetry, MaxAttempts=1) so it does not compound on the v1 retry, got %d", got)
	}
	if got := clientChunkSize(client); got != relevanceEmbedChunkSize {
		t.Fatalf("gate client chunk size must equal relevanceEmbedChunkSize (%d), got %d", relevanceEmbedChunkSize, got)
	}
}

// === F3: the production wiring split — both clients from one NewEmbedClients call ===

// TestRelevanceEmbedBudget_WiringSplit gates the PRODUCTION wiring site
// (main.go:initEngine), not a client the test constructs itself. The two
// client-level tests (NonGateClientKeepsLibraryDefaults,
// NoBespokePerRequestTimeout) each build their own client and so cannot catch
// a one-line regression at the call site — passing the gate's opts to the
// shared client in main.go builds fine and the whole suite stays green,
// reintroducing the exact defect this PR exists to prevent (the #418 shape:
// the feeder is tested, the call site is not).
//
// This test calls jobserver.NewEmbedClients — the function main.go calls — and
// asserts BOTH clients come from that one call with the correct, DISTINCT
// opts:
//
//   - gate:   per-request == 30s (library default, no bespoke timeout), retry
//     == NoRetry (MaxAttempts=1), chunk size == relevanceEmbedChunkSize;
//   - shared: retry == defaultRetryPolicy (MaxAttempts=3), per-request == 30s
//     (kitembed library defaults).
//
// Falsification: in NewEmbedClients, build the shared client with
// append(baseOpts, EmbedClientBudgetOpts()...) instead of baseOpts alone (the
// one-line regression). The shared client's MaxAttempts becomes 1 → the
// shared retry assertion RED.
func TestRelevanceEmbedBudget_WiringSplit(t *testing.T) {
	srv := newEmbedTestServer(t, 3)

	clients := NewEmbedClients(srv.URL,
		kitembed.WithBackend("http"),
		kitembed.WithDim(3),
	)
	if clients.GateErr != nil {
		t.Fatalf("NewEmbedClients gate: %v", clients.GateErr)
	}
	if clients.SharedErr != nil {
		t.Fatalf("NewEmbedClients shared: %v", clients.SharedErr)
	}

	// Gate client: no bespoke per-request timeout (library default 30s), v2
	// retry disabled (NoRetry), chunk size bound to the gate.
	if got := clientHTTPTimeout(clients.Gate); got != 30*time.Second {
		t.Fatalf("wiring split: gate client must use the library default 30s per-request timeout (no bespoke WithTimeout), got %v — EmbedClientBudgetOpts reintroduced a derived per-request timeout", got)
	}
	if got := clientRetryMaxAttempts(clients.Gate); got != 1 {
		t.Fatalf("wiring split: gate client must disable v2 retry (NoRetry, MaxAttempts=1), got %d", got)
	}
	if got := clientChunkSize(clients.Gate); got != relevanceEmbedChunkSize {
		t.Fatalf("wiring split: gate client chunk size must equal relevanceEmbedChunkSize (%d), got %d", relevanceEmbedChunkSize, got)
	}

	// Shared client: kitembed library defaults — the gate's opts MUST NOT
	// leak onto it. This is the assertion the call-site-untested gap let pass.
	if got := clientRetryMaxAttempts(clients.Shared); got != 3 {
		t.Fatalf("wiring split: shared client must keep the default retry policy (MaxAttempts=3), got %d — the gate's WithRetry(NoRetry) leaked onto the shared client at the wiring site", got)
	}
	if got := clientHTTPTimeout(clients.Shared); got != 30*time.Second {
		t.Fatalf("wiring split: shared client must keep the default 30s per-request timeout, got %v — a gate opt leaked onto the shared client at the wiring site", got)
	}
}
