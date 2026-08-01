package jobserver

import (
	"time"

	"github.com/anatolykoptev/go-kit/env"
	"github.com/anatolykoptev/go-kit/rerank"
)

// crossEncoderModel is the model name sent to the rerank server's
// POST /v1/rerank endpoint. The reranker lives on the SAME host and port as
// the embedder (EMBED_URL), verified live 2026-08-01: 3 documents, HTTP 200,
// 104 ms. The model is a constant, not env-tunable — it is paired with the
// server deployment, not an operator preference.
const crossEncoderModel = "gte-multi-rerank"

// crossEncoderMidpoint is the score at which the cross-encoder's keep/reject
// decision would flip. The gte-multi-rerank model scores in [0,1]; 0.5 is the
// model's own midpoint. The SHADOW agreement counter compares the cosine
// decision to what a cross-encoder decision at this midpoint would have been.
// This is NOT the threshold the gate will eventually use — that is set in a
// follow-up from the observed score distribution (this PR's payload). 0.5 is
// the neutral reference point for the agreement/disagreement measurement only.
const crossEncoderMidpoint = 0.5

// jobSearchCrossEncoderTimeout bounds the cross-encoder shadow call, derived
// from the PARENT tool context (NOT the gate context, which the embed calls
// may have nearly exhausted). The shadow is non-fatal: a timeout records the
// degraded reason and the gate returns exactly as it does today. Default 10s
// — the live probe returned in 104 ms for 3 docs; 10s is a generous bound for
// a full candidate set (up to 32) under load. Env-tunable following the
// JOB_SEARCH_RELEVANCE_TIMEOUT naming convention.
var jobSearchCrossEncoderTimeout = env.Duration("JOB_SEARCH_CROSSENCODER_TIMEOUT", 10*time.Second)

// relevanceRerankClient is the cross-encoder client OWNED by the relevance
// gate's shadow observer. It is DISTINCT from the embed client
// (relevanceEmbedClient): a different model, a different endpoint
// (/v1/rerank vs the embed endpoint), and a different failure contract (the
// embed client's failure fails the gate open; the rerank client's failure is
// silent and non-fatal — the gate's decision is unchanged).
//
// Set by main.go via SetRelevanceRerankClient; tests install fakes via
// withRelevanceReranker. The gate reads it via getRelevanceRerankClient.
// nil = the shadow is not configured (EMBED_URL unset) and skips silently.
var relevanceRerankClient *rerank.Client

// SetRelevanceRerankClient sets the relevance gate's cross-encoder shadow
// client. Use this at the production construction site (main.go) with a client
// built via NewRelevanceRerankClient so the model and timeout are scoped to
// the shadow. nil is valid and means the shadow is disabled.
func SetRelevanceRerankClient(c *rerank.Client) { relevanceRerankClient = c }

// getRelevanceRerankClient returns the relevance gate's cross-encoder shadow
// client (nil if not configured). Used by the shadow observer in
// applyRelevanceGate.
func getRelevanceRerankClient() *rerank.Client { return relevanceRerankClient }

// NewRelevanceRerankClient constructs the cross-encoder shadow client for the
// relevance gate. The URL is the SAME as the embed server (EMBED_URL) — the
// reranker lives on the same host:port, exposing POST /v1/rerank. The model is
// crossEncoderModel (gte-multi-rerank).
//
// The client is configured to mirror the embed client's discipline:
//   - WithRetry(rerank.NoRetry): the shadow is best-effort; one attempt. A
//     retry storm on a slow reranker would burn the tool budget for a signal
//     that changes nothing. The embed client uses the same NoRetry discipline.
//   - WithTimeout(jobSearchCrossEncoderTimeout): a per-request HTTP timeout
//     applied via context.WithTimeout. The shadow call also wraps its own
//     context (derived from the parent, not the gate context) with the same
//     bound, so the shadow cannot outlive its budget even if the parent
//     context is long-lived.
//   - MaxDocs is left at go-kit's own default (defaultMaxDocs=50): the
//     candidate set is capped at maxRelevanceCandidates (32) < 50, so the
//     whole set ships in one request. The server declares
//     RERANKER_BATCH_MAX=8; if the server rejects a larger batch, the shadow
//     fails gracefully (degraded counter) — the measurement PR reveals
//     whether a client-side cap matching the server is needed.
//
// url empty → returns nil (shadow disabled). This mirrors the embed client's
// absent-config behaviour (EMBED_URL empty → no client → gate fails open).
func NewRelevanceRerankClient(url string) *rerank.Client {
	if url == "" {
		return nil
	}
	return rerank.NewClient(url,
		rerank.WithModel(crossEncoderModel),
		rerank.WithTimeout(jobSearchCrossEncoderTimeout),
		rerank.WithRetry(rerank.NoRetry),
	)
}
