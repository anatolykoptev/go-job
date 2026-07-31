package jobserver

import (
	"log/slog"
	"time"

	kitembed "github.com/anatolykoptev/go-kit/embed"
)

// relevanceEmbedChunkSize is the max texts per embed round-trip. It matches
// the embed server's EMBED_MAX_INPUT_ARRAY cap (32) and kitembed's
// defaultChunkSize, so a candidate set of this size is exactly ONE chunk —
// one sequential round-trip, not two. maxRelevanceCandidates is set equal to
// this so the cap and the chunk size cannot drift apart (fix B): the
// candidate set is always a single chunk.
const relevanceEmbedChunkSize = 32

// The relevance gate makes two sequential embed round-trips inside one
// jobSearchRelevanceTimeout context: EmbedQuery (the query) then Embed (the
// candidate passages). With the candidate cap set to relevanceEmbedChunkSize
// the passage call is a single chunk, so the gate issues exactly
// relevanceEmbedChunks round-trips total.
const relevanceEmbedChunks = 2

// The effective retry is kitembed's HTTPEmbedder.withRetry (the v1 internal
// retry): 3 attempts with 200ms→400ms backoff (2 sleeps for 3 attempts =
// 600ms). The v2 Client retry is disabled via WithRetry(NoRetry) below so it
// does not compound on top of the v1 layer (3×3 = 9 attempts), which would
// make the worst case unbounded relative to the gate budget. The v1 retry is
// not configurable through opts, so it is the floor this budget accounts for.
const (
	relevanceEmbedAttempts = 3
	relevanceEmbedBackoff  = 200*time.Millisecond + 400*time.Millisecond
)

// relevanceEmbedPerRequest is the per-request HTTP timeout, derived from the
// gate budget so the FULL retry envelope for both round-trips fits strictly
// inside jobSearchRelevanceTimeout (fix A — the pre-fix code passed no
// WithTimeout, leaving kitembed's 30s default, 2× the 15s gate budget, so the
// gate deadline was the only timeout that could ever fire and the retry policy
// could not complete inside the gate):
//
//	worst_case = relevanceEmbedChunks × (relevanceEmbedAttempts × perRequest + relevanceEmbedBackoff)
//	constraint: worst_case < jobSearchRelevanceTimeout
//
// Solving for perRequest and taking 80% as a safety margin (jitter, the two
// calls sharing one context, and the scoring work between them):
//
//	perRequest = ((gate / chunks - backoff) / attempts) × 0.8
//
// For the shipped 15s gate this is ((7.5s - 0.6s) / 3) × 0.8 ≈ 1.84s, giving a
// worst case of ~12.2s < 15s. TestRelevanceEmbedBudget_InvariantFitsGateTimeout
// asserts the strict inequality for the configured budget, and mutates
// perRequest to the gate budget to prove the assertion is not vacuous.
var relevanceEmbedPerRequest = computeEmbedPerRequest(jobSearchRelevanceTimeout)

func computeEmbedPerRequest(gate time.Duration) time.Duration {
	ceiling := (gate/relevanceEmbedChunks - relevanceEmbedBackoff) / relevanceEmbedAttempts
	perRequest := ceiling * 4 / 5 // 80% safety margin
	if perRequest <= 0 {
		// The gate budget is too small to fit the retry envelope. Clamp so
		// kitembed's 30s default is never silently restored (WithHTTPTimeout
		// ignores d<=0); the invariant test still catches the misconfiguration
		// for the shipped default, and a too-tight gate fails open (safe).
		slog.Warn("job_search: JOB_SEARCH_RELEVANCE_TIMEOUT too small for the embed retry envelope; clamping per-request to 200ms",
			slog.Duration("gate", gate),
			slog.Duration("ceiling", ceiling))
		perRequest = 200 * time.Millisecond
	}
	return perRequest
}

// worstCaseEmbedEnvelope returns the worst-case wall-clock time the gate's
// embed work can consume: every round-trip exhausting every retry attempt at
// the per-request timeout, plus all backoff. The invariant this package
// enforces is worstCaseEmbedEnvelope() < jobSearchRelevanceTimeout.
func worstCaseEmbedEnvelope() time.Duration {
	return time.Duration(relevanceEmbedChunks) *
		(time.Duration(relevanceEmbedAttempts)*relevanceEmbedPerRequest + relevanceEmbedBackoff)
}

// EmbedClientBudgetOpts returns kitembed options that bind the embed client's
// per-request timeout, retry, and chunk size to jobSearchRelevanceTimeout so
// the gate's inner budgets fit strictly inside its outer budget. Apply these
// LAST at the construction site so they win over any caller-supplied
// timeout/retry/chunk options.
func EmbedClientBudgetOpts() []kitembed.Opt {
	return []kitembed.Opt{
		kitembed.WithTimeout(relevanceEmbedPerRequest),
		kitembed.WithRetry(kitembed.NoRetry),
		kitembed.WithChunkSize(relevanceEmbedChunkSize),
	}
}

// NewEmbedClient constructs the embed client used by the job_search relevance
// gate, with per-request/retry/chunk budgets derived from
// jobSearchRelevanceTimeout. baseOpts select the backend, dimension, and
// logger; the budget opts are appended last so they win. Use this at the
// production construction site (main.go) so the gate's inner budgets cannot
// drift apart from its outer budget — the defect this fixes.
func NewEmbedClient(url string, baseOpts ...kitembed.Opt) (*kitembed.Client, error) {
	opts := append([]kitembed.Opt{}, baseOpts...)
	opts = append(opts, EmbedClientBudgetOpts()...)
	return kitembed.NewClient(url, opts...)
}
