package jobserver

import (
	kitembed "github.com/anatolykoptev/go-kit/embed"
)

// relevanceEmbedChunkSize is the max texts per embed round-trip. It matches
// the embed server's EMBED_MAX_INPUT_ARRAY cap (32) and kitembed's
// defaultChunkSize, so a candidate set of this size is exactly ONE chunk —
// one sequential round-trip, not two. maxRelevanceCandidates is set equal to
// this so the cap and the chunk size cannot drift apart (fix B): the
// candidate set is always a single chunk.
const relevanceEmbedChunkSize = 32

// EmbedClientBudgetOpts returns kitembed options that scope the relevance
// gate's embed client. Apply these LAST at the construction site so they win
// over any caller-supplied retry/chunk options.
//
// The per-request HTTP timeout is deliberately NOT tuned here. The OUTER
// bound is the gate context — jobSearchRelevanceTimeout (15s default, set in
// relevance_gate.go), which wraps both embed calls (EmbedQuery then Embed)
// via context.WithTimeoutCause. It is NOT the sole arbiter of when the gate
// gives up: context.WithTimeoutCause yields min(d, parent's remaining), so
// the gate gives up when EITHER its own budget OR the parent's deadline
// expires, whichever is first; the two are distinguished via context.Cause
// in classifyEmbedError. The per-request timeout is kitembed's library
// default (30s), the same default every other caller of the same embed
// server uses (go-search 30s explicit, MemDB 30s default, vaelor 120s
// explicit — none derive one).
//
// A per-request timeout TIGHTER than the feature's own outer budget can only
// convert a graceful degradation into a hard failure: the prior derived
// 1.84s timeout fired on every gate call before the server responded
// ("Client.Timeout exceeded while awaiting headers"), so the gate never
// completed. The library default lets the gate context be the sole arbiter.
//
// WithRetry(NoRetry) disables the v2 Client retry policy (the RetryPolicy on
// the *Client) so it does not compound on the v1 ladder. It does NOT disable
// retries outright: the v1 ladder inside HTTPEmbedder.Embed (http.go:124,
// defaultRetry at retry.go:166) is always on — 3 attempts, 200ms→400ms
// backoff — and no kitembed opt reaches it. WithRetry(NoRetry) keeps the
// total attempt count at 3 (the v1 floor), not 9 (v2×v1 compounding).
//
// WithChunkSize(relevanceEmbedChunkSize) makes the passage call a single
// round-trip (one chunk = one sequential request), matching the server cap.
func EmbedClientBudgetOpts() []kitembed.Opt {
	return []kitembed.Opt{
		kitembed.WithRetry(kitembed.NoRetry),
		kitembed.WithChunkSize(relevanceEmbedChunkSize),
	}
}

// NewEmbedClient constructs the embed client used by the job_search relevance
// gate, with retry and chunk size scoped to the gate (EmbedClientBudgetOpts).
// The per-request timeout is kitembed's library default. The gate context
// (jobSearchRelevanceTimeout) bounds the gate's own work but is NOT the sole
// outer bound: WithTimeoutCause yields min(d, parent's remaining), so the
// parent tool context can expire first — classifyEmbedError tells the two
// apart. baseOpts select the
// backend, dimension, and logger; the budget opts are appended last so they
// win. Use this at the production construction site (main.go).
func NewEmbedClient(url string, baseOpts ...kitembed.Opt) (*kitembed.Client, error) {
	opts := append([]kitembed.Opt{}, baseOpts...)
	opts = append(opts, EmbedClientBudgetOpts()...)
	return kitembed.NewClient(url, opts...)
}

// relevanceEmbedClient is the embed client OWNED by the relevance gate —
// constructed via NewEmbedClient (which applies EmbedClientBudgetOpts) so its
// retry and chunk size are scoped to the gate.
//
// It is DISTINCT from the package-level singleton in jobs
// (jobs.SetEmbedClient/GetEmbedClient), which serves algora ingest,
// resume-vector sync, and profile sync and MUST keep kitembed's library
// defaults (defaultRetryPolicy: 3 attempts; 30s per-request timeout). Binding
// the gate's opts to that shared singleton would leak WithRetry(NoRetry)
// onto those background jobs — a visible gate change traded for an invisible
// ingest reliability regression (one 503 during resume ingest failing on the
// first attempt where it previously retried).
//
// Set by main.go via SetRelevanceEmbedClient; tests install fakes via
// withRelevanceEmbedder. The gate reads it via getRelevanceEmbedClient.
var relevanceEmbedClient kitembed.Embedder

// SetRelevanceEmbedClient sets the relevance gate's own embed client. Use this
// at the production construction site (main.go) with a client built via
// NewEmbedClient so the gate's opts are scoped to the gate, not the shared
// singleton consumed by background ingest.
func SetRelevanceEmbedClient(c kitembed.Embedder) { relevanceEmbedClient = c }

// getRelevanceEmbedClient returns the relevance gate's embed client (nil if
// not configured). Used by applyRelevanceGate; distinct from jobs.GetEmbedClient.
func getRelevanceEmbedClient() kitembed.Embedder { return relevanceEmbedClient }
