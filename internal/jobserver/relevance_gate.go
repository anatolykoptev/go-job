package jobserver

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	kitembed "github.com/anatolykoptev/go-kit/embed"
	"github.com/anatolykoptev/go-kit/env"
	"github.com/anatolykoptev/go-kit/rerank"
	"github.com/anatolykoptev/go_job/internal/engine"
)

// Relevance gate configuration (env-tunable, validated at init).
//
// jobSearchMinRelevance is the cosine-similarity threshold below which a
// candidate is rejected. Default 0.0 — the gate SCORES every candidate
// (writing cosine into Score, sorted desc) but does NOT reject by default.
//
// The 0.80 value previously shipped here was not defensible. The honest
// calibration (~/tmp/relevance_before.txt + the M1 finding) showed the
// same-query margin on e5-large is 0.033 (off-topic 0.7935 → on-topic
// 0.8269): e5-large cosines sit in a narrow high band, so a single
// percentage point of noise flips the classification. No threshold in that
// gap is stable, and a threshold nobody can defend is worse than an explicit
// "this needs a better signal" because it will be trusted. An operator who
// calibrates against their own embedder + corpus sets JOB_SEARCH_MIN_RELEVANCE;
// with minKeep=0 the gate is then a true hard gate (B1).
//
// jobSearchMinKeep is the floor: if fewer than this many candidates pass the
// threshold, the top-N by score are kept anyway. Default 0 — a true hard gate.
// Returning nothing is a correct and useful answer to "are there jobs matching
// this?". When the floor IS engaged (an operator sets it > 0), the survivors
// are flagged as a DISTINCT visible state (floor_kept outcome + a caller
// notice), never silently indistinguishable from a real match (B2).
//
// jobSearchRelevanceTimeout bounds the gate's own embed work, independent of
// the tool context. It is the OUTER bound: it wraps both embed calls
// (EmbedQuery then Embed) via context.WithTimeoutCause. It is NOT the sole
// arbiter — context.WithTimeout yields min(d, parent's remaining), so the
// gate gives up when EITHER its own budget OR the parent's deadline expires,
// whichever is first; the two are distinguished via context.Cause. The embed
// client's per-request timeout is kitembed's library default (see
// relevance_embed.go); a per-request timeout tighter than this outer budget
// converts a graceful degradation into a hard failure.
var (
	jobSearchMinRelevance     = env.Float("JOB_SEARCH_MIN_RELEVANCE", 0.0)
	jobSearchMinKeep          = env.Int("JOB_SEARCH_MIN_KEEP", 0)
	jobSearchRelevanceTimeout = env.Duration("JOB_SEARCH_RELEVANCE_TIMEOUT", 15*time.Second)
)

// errGateBudget is the cause set on the gate's own context deadline via
// context.WithTimeoutCause. When the GATE's jobSearchRelevanceTimeout expires
// first, context.Cause(gateCtx) returns this sentinel; when the PARENT tool
// context's deadline expires first, context.Cause returns the parent's cause
// (or the default context.DeadlineExceeded), never this sentinel. That is the
// distinction classifyEmbedError uses to label timeout_gate vs timeout_parent
// — two failures that previously shared the single "timeout" label and call
// for opposite fixes (raise the gate budget vs leave the gate more of the
// parent's remaining time).
var errGateBudget = errors.New("relevance gate: own budget expired")

func init() {
	validateRelevanceConfig()
}

// relevanceGateInert reports whether the gate is configured to reject nothing
// (minRelevance <= 0 and minKeep <= 0). At shipped defaults the gate SCORES
// every candidate but filters none, so a degraded gate and a healthy gate
// produce identical user-facing output. Used by the summary builder to
// suppress the "Relevance filtering unavailable" notice when it would be
// alarming noise that distinguishes nothing (fix C). The log line and the
// job_search_relevance_degraded_total counter are unaffected — only the
// user-facing summary string is gated on this.
func relevanceGateInert() bool {
	return jobSearchMinRelevance <= 0 && jobSearchMinKeep <= 0
}

// validateRelevanceConfig parses the env vars with error reporting and range
// checks. env.Float/env.Int silently fall back to the default on a malformed
// value with NO signal, so JOB_SEARCH_MIN_RELEVANCE=1.5 would make every
// search return nothing (or, with a floor, exactly N wrong things) forever.
// On a bad value: log loudly at WARN with the offending input and fall back
// to the default (M1).
func validateRelevanceConfig() {
	if v, ok := env.Lookup("JOB_SEARCH_MIN_RELEVANCE"); ok && v != "" {
		f, err := parseFloat64(v)
		switch {
		case err != nil:
			slog.Warn("job_search: invalid JOB_SEARCH_MIN_RELEVANCE, using default",
				slog.String("value", v),
				slog.Float64("default", 0.0),
				slog.Any("error", err))
			jobSearchMinRelevance = 0.0
		case f < 0 || f > 1:
			slog.Warn("job_search: JOB_SEARCH_MIN_RELEVANCE out of range [0,1], using default",
				slog.String("value", v),
				slog.Float64("got", f),
				slog.Float64("default", 0.0))
			jobSearchMinRelevance = 0.0
		default:
			jobSearchMinRelevance = f
		}
	}
	if v, ok := env.Lookup("JOB_SEARCH_MIN_KEEP"); ok && v != "" {
		n, err := parseInt(v)
		if err != nil || n < 0 {
			slog.Warn("job_search: invalid JOB_SEARCH_MIN_KEEP (must be non-negative int), using default",
				slog.String("value", v),
				slog.Int("default", 0),
				slog.Any("error", err))
			jobSearchMinKeep = 0
		} else {
			jobSearchMinKeep = n
		}
	}
}

// maxSnippetRunes bounds the snippet length embedded per candidate. Full page
// text is not embedded — only title + a bounded snippet of Content, matching
// what production scores (M1: the prior harness embedded title|company|location
// instead, so its measured distribution did not describe the scored text).
const maxSnippetRunes = 500

// maxRelevanceCandidates caps the number of candidates embedded before
// scoring. deduped is pre-limit and can carry up to 18 connectors' worth; an
// unbounded set can burn the whole tool budget (M3). The cap is set equal to
// relevanceEmbedChunkSize (the embed server's EMBED_MAX_INPUT_ARRAY cap and
// kitembed's chunk size) so the candidate set is exactly ONE upstream chunk —
// one sequential round-trip, not two (fix B). Expressing the cap as the chunk
// size keeps the two from drifting apart: a cap above the chunk size would
// reintroduce the second round-trip the gate budget no longer accounts for.
// When the cap trims, that is a visible degraded state (truncated reason + a
// caller notice), not silence.
const maxRelevanceCandidates = relevanceEmbedChunkSize

// applyRelevanceGate scores every candidate against the query via cosine
// similarity (embedder + rerank.MathReranker with Lambda=0 for pure cosine
// sort), writes the score into each result's Score field, and filters by
// engine.FilterByScore.
//
// Fail-open contract: if the embedder is not configured, returns an error,
// times out, has its circuit breaker open, or returns empty/mismatched/per-item
// -empty vectors, the input results are returned UNFILTERED (in their original
// order) and a non-empty degraded reason string is returned. The caller makes
// the degradation visible in the output summary and bumps the
// job_search_relevance_degraded_total{reason} metric.
//
// Floor contract (B2): when minKeep > 0 and fewer than minKeep candidates pass
// the threshold, the top-minKeep by score are kept anyway, but flagged as a
// DISTINCT state — the floor_kept metric outcome is bumped for the survivors
// (not kept), and a non-empty notice string is returned so the caller can tell
// the user these results did not meet the relevance bar. floor_survivors are
// never silently indistinguishable from threshold-passers.
//
// Returns (filteredResults, degradedReason, notice). degradedReason is "" when
// the gate ran successfully (even if it kept everything via threshold or
// floor); notice is "" when no floor engaged and no truncation happened.
func applyRelevanceGate(ctx context.Context, query string, results []engine.SearxngResult) ([]engine.SearxngResult, string, string) {
	if len(results) == 0 {
		return results, "", ""
	}

	ec := getRelevanceEmbedClient()
	if ec == nil {
		engine.IncrJobSearchRelevanceDegraded(engine.RelevanceReasonNotConfigured)
		slog.Info("job_search: relevance gate skipped — embedder not configured")
		return results, engine.RelevanceReasonNotConfigured, ""
	}

	// M3: own short timeout so the gate cannot burn the whole tool budget.
	// The embed client's per-request/retry/chunk budgets are derived from
	// this timeout (see relevance_embed.go); without that derivation a slow
	// request or transient retryable failure surfaces as a late "LLM
	// summarization failed" blaming the wrong component.
	//
	// WithTimeoutCause (not WithTimeout) sets errGateBudget as the cause so
	// classifyEmbedError can tell the gate's own budget expiring apart from
	// the parent tool context's deadline expiring first — via context.Cause,
	// not ctx.Err() alone (which is DeadlineExceeded in both cases).
	gateCtx, cancel := context.WithTimeoutCause(ctx, jobSearchRelevanceTimeout, errGateBudget)
	defer cancel()

	// M3: cap candidates embedded. When trimmed, mark a visible degraded state
	// (the gate still runs on the capped set — this is NOT fail-open).
	var notice string
	candidates := results
	if len(results) > maxRelevanceCandidates {
		engine.IncrJobSearchRelevanceDegraded(engine.RelevanceReasonTruncated)
		slog.Warn("job_search: relevance gate capped candidates",
			slog.Int("candidates", len(results)),
			slog.Int("cap", maxRelevanceCandidates))
		candidates = results[:maxRelevanceCandidates]
		notice = "relevance gate scored only the first " + strconv.Itoa(maxRelevanceCandidates) + " of " + strconv.Itoa(len(results)) + " candidates (cap)"
	}

	// Build document strings: title + bounded snippet of Content — exactly
	// what production scores (M1). passage:/query: prefixes match the embed
	// server's instruction-tuned e5 convention.
	docs := make([]rerank.Doc, len(candidates))
	passages := make([]string, len(candidates))
	for i, r := range candidates {
		doc := r.Title
		if r.Content != "" {
			doc = doc + " " + engine.TruncateRunes(r.Content, maxSnippetRunes, "")
		}
		passages[i] = "passage: " + doc
		// Text carries the prefixed passage so the doc is self-describing;
		// MathReranker scores via EmbedVector, not Text, but keeping them
		// consistent avoids a misleading asymmetry (minor).
		docs[i] = rerank.Doc{ID: r.URL, Text: passages[i]}
	}

	// Embed query once.
	qvec, err := ec.EmbedQuery(gateCtx, "query: "+query)
	if err != nil {
		reason := classifyEmbedError(err, gateCtx)
		engine.IncrJobSearchRelevanceDegraded(reason)
		slog.Warn("job_search: relevance gate degraded — embed query failed",
			slog.Any("error", err),
			slog.String("reason", reason))
		return results, reason, ""
	}
	if len(qvec) == 0 {
		engine.IncrJobSearchRelevanceDegraded(engine.RelevanceReasonEmptyVectors)
		slog.Warn("job_search: relevance gate degraded — empty query vector")
		return results, engine.RelevanceReasonEmptyVectors, ""
	}

	// Embed all passages in one batch.
	vecs, err := ec.Embed(gateCtx, passages)
	if err != nil {
		reason := classifyEmbedError(err, gateCtx)
		engine.IncrJobSearchRelevanceDegraded(reason)
		slog.Warn("job_search: relevance gate degraded — embed passages failed",
			slog.Any("error", err),
			slog.String("reason", reason))
		return results, reason, ""
	}
	if len(vecs) < len(candidates) {
		engine.IncrJobSearchRelevanceDegraded(engine.RelevanceReasonEmptyVectors)
		slog.Warn("job_search: relevance gate degraded — partial embeddings",
			slog.Int("got", len(vecs)),
			slog.Int("want", len(candidates)))
		return results, engine.RelevanceReasonEmptyVectors, ""
	}

	// M5: a per-item empty vector inside a full-length response is NOT caught
	// by the len check above. Without this, EmbedVector stays nil, MathReranker
	// scores it 0, and a real match is silently reclassified as irrelevant
	// with degraded="". Detect any empty per-item vector and treat it as
	// degradation (fail-open), not as a zero score.
	for i := range vecs {
		if len(vecs[i]) == 0 {
			engine.IncrJobSearchRelevanceDegraded(engine.RelevanceReasonEmptyVectors)
			slog.Warn("job_search: relevance gate degraded — per-item empty vector",
				slog.Int("index", i))
			return results, engine.RelevanceReasonEmptyVectors, ""
		}
	}

	// Attach vectors to docs.
	for i := range docs {
		docs[i].EmbedVector = vecs[i]
	}

	// Score with MathReranker (Lambda=0 → pure cosine sort).
	rr := rerank.MathReranker{QueryVector: qvec, Lambda: 0}
	scored := rr.Rerank(gateCtx, query, docs)

	// Write cosine into each result's Score field, in scored (desc) order.
	sorted := make([]engine.SearxngResult, 0, len(scored))
	for _, s := range scored {
		idx := s.OrigRank
		if idx < 0 || idx >= len(candidates) {
			continue
		}
		candidates[idx].Score = float64(s.Score)
		sorted = append(sorted, candidates[idx])
	}

	// Count how many actually passed the threshold (needed to distinguish
	// threshold-kept from floor-kept — B2).
	passingCount := 0
	for _, r := range sorted {
		if r.Score >= jobSearchMinRelevance {
			passingCount++
		}
	}

	// Gate with the in-repo seam engine.FilterByScore (minor: the prior code
	// imported go-engine/websearch directly while the repo re-exports the same
	// function as engine.FilterByScore, which is what pipeline.go uses).
	filtered := engine.FilterByScore(sorted, jobSearchMinRelevance, jobSearchMinKeep)

	// Metrics: scored for all (reg.Add bulk, not a per-item Incr loop — minor);
	// kept for threshold-passers; floor_kept for floor survivors; rejected
	// for the rest. Counts derive from passingCount + len(filtered) directly
	// (minor: the prior O(n*m) scan used float equality on Score to derive
	// the rejected count).
	if n := len(sorted); n > 0 {
		engine.AddJobSearchRelevance(engine.RelevanceScored, n)
	}

	// Did the floor engage? FilterByScore has TWO floor branches: it returns
	// results[:minKeep] when len(sorted) >= minKeep, and the WHOLE slice when
	// there are fewer candidates than the floor. Conditioning on the first
	// branch alone let the second return rejected results as if nothing had
	// been floored. len(filtered) > passingCount covers both.
	floorEngaged := jobSearchMinKeep > 0 && passingCount < jobSearchMinKeep && len(filtered) > passingCount
	floorKeptCount := 0
	if floorEngaged {
		floorKeptCount = len(filtered) - passingCount
	}

	if passingCount > 0 {
		engine.AddJobSearchRelevance(engine.RelevanceKept, passingCount)
	}
	if floorKeptCount > 0 {
		engine.AddJobSearchRelevance(engine.RelevanceFloorKept, floorKeptCount)
	}
	rejectedCount := len(sorted) - len(filtered)
	if rejectedCount > 0 {
		engine.AddJobSearchRelevance(engine.RelevanceRejected, rejectedCount)
	}

	// B2: surface the floor as a distinct caller-visible state.
	if floorEngaged {
		floorNotice := "relevance floor engaged: " + strconv.Itoa(floorKeptCount) + " result(s) kept below the relevance bar (" + strconv.FormatFloat(jobSearchMinRelevance, 'f', 3, 64) + ") — these did not meet the threshold"
		notice = appendNotice(notice, floorNotice)
	}

	// Score distribution — the threshold ships at 0.0 because none could be
	// honestly measured; these three numbers let real traffic supply it.
	var scoreMin, scoreMed, scoreMax float64
	if n := len(sorted); n > 0 {
		scoreMax = sorted[0].Score
		scoreMin = sorted[n-1].Score
		scoreMed = sorted[n/2].Score
	}
	slog.Info("job_search: relevance gate applied",
		slog.Int("candidates", len(sorted)),
		slog.Int("kept", passingCount),
		slog.Int("floor_kept", floorKeptCount),
		slog.Int("rejected", rejectedCount),
		slog.Float64("min_relevance", jobSearchMinRelevance),
		slog.Int("min_keep", jobSearchMinKeep),
		slog.Float64("score_min", scoreMin),
		slog.Float64("score_median", scoreMed),
		slog.Float64("score_max", scoreMax))

	// SHADOW MODE: score every candidate with the cross-encoder (gte-multi-
	// rerank) and record metrics — but NEVER change the keep/reject decision.
	// The gate's return value (filtered) is already final; the shadow only
	// observes. Failure is non-fatal and invisible to the caller.
	//
	// The shadow is dispatched ASYNC: it does not block the gate's return
	// (measured 1.12 s for 32 docs on the live reranker — that latency is
	// visible to the caller, violating the "invisible" contract). The
	// dispatch is bounded (semaphore) and detached (context.WithoutCancel)
	// so it survives request cancellation and cannot pile up without bound.
	dispatchCrossEncoderShadow(ctx, query, sorted, filtered)

	return filtered, "", notice
}

// classifyEmbedError maps an embedder error to a bounded degraded-reason label.
// circuit_open → embed.ErrCircuitOpen; timeout is split into timeout_gate (the
// gate's own jobSearchRelevanceTimeout expired, signalled by errGateBudget on
// context.Cause(gateCtx)) vs timeout_parent (the parent tool context's deadline
// expired first); else embed_error.
//
// circuit_open is checked BEFORE the deadline arms so that a retry ladder
// returning a *retry.RetryError{ctxErr: DeadlineExceeded, lastErr: ErrCircuitOpen}
// — the shape go-kit v0.97.11 produces when the circuit is open and the gate
// context then expires during the retry backoff — classifies as circuit_open,
// not timeout. Before v0.97.11 the retry ladder dropped the causal error
// (fmt.Errorf with %w on ctx.Err, %v on the attempt error), so this case was
// misclassified as timeout.
func classifyEmbedError(err error, gateCtx context.Context) string {
	if errors.Is(err, kitembed.ErrCircuitOpen) {
		return engine.RelevanceReasonCircuitOpen
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		// context.Cause distinguishes whose deadline expired: errGateBudget
		// is set only by the gate's own WithTimeoutCause, so its presence
		// means the gate's budget ran out; its absence means the parent's
		// deadline expired first (the parent's cause propagates, or nil when
		// the parent was cancelled without a cause). ctx.Err() is
		// DeadlineExceeded in both cases — it cannot make this distinction.
		if errors.Is(context.Cause(gateCtx), errGateBudget) {
			return engine.RelevanceReasonTimeoutGate
		}
		return engine.RelevanceReasonTimeoutParent
	}
	return engine.RelevanceReasonEmbedError
}

// appendNotice joins two notice fragments with "; ".
func appendNotice(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "; " + b
}

// parseFloat64/parseInt parse the raw env string so a malformed value is
// reported with the offending input (env.Float/env.Int swallow the error).
func parseFloat64(s string) (float64, error) {
	return strconv.ParseFloat(s, 64)
}

func parseInt(s string) (int, error) {
	return strconv.Atoi(s)
}

// maxConcurrentShadowReranks bounds the number of cross-encoder shadow
// observations that may run concurrently. The reranker declares
// MAX_CONCURRENT_RERANK_REQUESTS=4; a goroutine per request under load must
// not accumulate without bound. When the bound is hit, the sample is DROPPED
// and counted under shadow_dropped — correct for a shadow that samples a
// distribution, not one that audits every request.
const maxConcurrentShadowReranks = 4

// shadowSem is the bounded semaphore for concurrent shadow observations.
var shadowSem = make(chan struct{}, maxConcurrentShadowReranks)

// shadowScheduler schedules the detached shadow work. Production: launches a
// goroutine. Tests replace with a synchronous runner (func(work func()) {
// work() }) so metric assertions are deterministic without sleeping. This is
// the one piece of production code that exists for test injectability — the
// alternative (a synchronization point the tests poll) is a flake, and a
// timer-based wait is explicitly prohibited. The tradeoff: one variable, one
// line, clearly documented.
var shadowScheduler = func(work func()) { go work() }

// dispatchCrossEncoderShadow schedules the cross-encoder shadow observation
// as detached, bounded, non-blocking work. It NEVER blocks the gate's return.
//
// The dispatch:
//  1. Records the candidate-set size histogram (once per gate application,
//     regardless of shadow success — this is the gate's input, not the
//     shadow's).
//  2. Checks the rerank client (nil/unavailable → not_configured, return).
//  3. Acquires the semaphore (non-blocking; full → shadow_dropped, return).
//  4. Creates a detached context (context.WithoutCancel) with the shadow's
//     own timeout bound, so the shadow survives request cancellation and
//     cannot outlive its budget.
//  5. Schedules the observation via shadowScheduler.
func dispatchCrossEncoderShadow(ctx context.Context, query string, scored []engine.SearxngResult, kept []engine.SearxngResult) {
	if len(scored) == 0 {
		return
	}

	// Candidate-set size histogram — once per gate application. Decides
	// whether a later PR needs a sparse/RRF pre-filter.
	engine.ObserveJobSearchCandidateSetSize(len(scored))

	rc := getRelevanceRerankClient()
	if rc == nil || !rc.Available() {
		engine.IncrJobSearchCrossEncoderDegraded(engine.CrossEncoderReasonNotConfigured)
		return
	}

	// Bound concurrent shadows. Non-blocking acquire: when the bound is hit,
	// drop the sample and count it under its own reason. Dropping is correct
	// for a shadow — this samples a distribution, it does not audit every
	// request.
	select {
	case shadowSem <- struct{}{}:
	default:
		engine.IncrJobSearchCrossEncoderDegraded(engine.CrossEncoderReasonShadowDropped)
		return
	}

	// Detached context: the shadow must not be killed when the request's
	// context ends (the caller has already returned). context.WithoutCancel
	// detaches from the parent's cancellation; WithTimeout applies the
	// shadow's own bound. The client no longer applies its own WithTimeout
	// (removed from NewRelevanceRerankClient — redundant with this bound).
	shadowCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), jobSearchCrossEncoderTimeout)
	shadowScheduler(func() {
		defer cancel()
		defer func() { <-shadowSem }()
		observeCrossEncoderShadow(shadowCtx, rc, query, scored, kept)
	})
}

// observeCrossEncoderShadow scores every candidate the gate already scored
// (cosine) with the cross-encoder (gte-multi-rerank) and records metrics ONLY.
// It NEVER changes the keep/reject decision — the gate's filtered result is
// already final when this runs. This is the SHADOW MODE measurement: the
// cross-encoder score distribution, the candidate-set size, and the
// agreement/disagreement between the cosine decision and a cross-encoder
// decision at the 0.5 midpoint.
//
// Failure is non-fatal and invisible to the caller: if the cross-encoder
// times out or errors, the degraded counter is bumped and the function
// returns. The gate's return value is unchanged.
//
// Missing scores: a document the reranker did not return a score for (the
// client fills unseen docs with Score=0) is NOT entered into the score
// histogram and does NOT vote in the agreement counter. It is counted
// separately under job_search_crossencoder_missing_scores_total. This keeps
// the histogram clean — every sample in it is a real score. The gte-multi-
// rerank model outputs sigmoid values in (0,1), never exactly 0, so Score=0
// reliably means "unseen" for this model. If a future change adds client-side
// normalization (MinMax could produce 0 for the lowest score), this
// classification would need revisiting.
//
// Document text fed to the cross-encoder: title + a bounded snippet of Content
// (maxSnippetRunes, the SAME text the gate embeds for cosine), WITHOUT the
// "passage:" prefix — that prefix is e5-embed-specific; the cross-encoder is a
// different model (gte-multi-rerank) and scores the raw text. Feeding the same
// text makes the cosine/cross-encoder comparison apples-to-apples.
//
// scored = all candidates the gate scored (cosine in .Score, desc order).
// kept = the gate's final filtered result (the keep decision, unchanged).
func observeCrossEncoderShadow(ctx context.Context, rc *rerank.Client, query string, scored []engine.SearxngResult, kept []engine.SearxngResult) {
	// Build docs from the scored candidates. Text = title + content snippet
	// (same as the embed path, minus the e5 "passage:" prefix).
	docs := make([]rerank.Doc, len(scored))
	for i, r := range scored {
		doc := r.Title
		if r.Content != "" {
			doc = doc + " " + engine.TruncateRunes(r.Content, maxSnippetRunes, "")
		}
		docs[i] = rerank.Doc{ID: r.URL, Text: doc}
	}

	res, err := rc.RerankWithResult(ctx, query, docs)
	if err != nil {
		reason := classifyCrossEncoderError(err)
		engine.IncrJobSearchCrossEncoderDegraded(reason)
		slog.Debug("job_search: cross-encoder shadow degraded",
			slog.Any("error", err),
			slog.String("reason", reason))
		return
	}

	// res is always non-nil (RerankWithResult never returns nil). Status is
	// always StatusOk here: StatusDegraded returns err != nil (caught above),
	// StatusSkipped is guarded by len(scored)>0 + rc.Available() in the
	// dispatch, and StatusFallback requires a fallback not configured. The
	// former res==nil || res.Status != StatusOk branch was unreachable dead
	// code — removed along with crossEncoderStatusReason/crossEncoderStatusString.

	// Map URL → cross-encoder score. Skip Score=0 entries: the client fills
	// unseen docs with Score=0, and the gte-multi-rerank model outputs sigmoid
	// values in (0,1) — never exactly 0. So Score=0 reliably means "the
	// reranker did not score this document." Excluding these from the map
	// makes the !ok check below the missing-score detector.
	xeScores := make(map[string]float32, len(res.Scored))
	for _, s := range res.Scored {
		if s.Score == 0 {
			continue
		}
		xeScores[s.ID] = s.Score
	}

	// Build the kept set (which scored candidates the gate kept). URLs are the
	// doc IDs — the same convention the embed path uses.
	keptURLs := make(map[string]bool, len(kept))
	for _, r := range kept {
		keptURLs[r.URL] = true
	}

	for _, r := range scored {
		xeScore, ok := xeScores[r.URL]
		if !ok {
			// Missing: the reranker did not score this document. Do NOT enter
			// the histogram, do NOT vote in the agreement counter. Count
			// separately so a future distribution can be read with confidence
			// that every sample is real.
			engine.IncrJobSearchCrossEncoderMissingScores()
			continue
		}
		engine.ObserveJobSearchCrossEncoderScore(float64(xeScore))

		cosineKept := keptURLs[r.URL]
		xeWouldKeep := float64(xeScore) >= crossEncoderMidpoint
		engine.IncrJobSearchRelevanceAgreement(agreementOutcome(cosineKept, xeWouldKeep))

		slog.Debug("job_search: cross-encoder shadow pair",
			slog.String("url", r.URL),
			slog.String("title", r.Title),
			slog.Float64("cosine", r.Score),
			slog.Float64("cross_encoder", float64(xeScore)),
			slog.Bool("cosine_kept", cosineKept),
			slog.Bool("xe_would_keep", xeWouldKeep))
	}
}

// agreementOutcome maps the (cosineKept, xeWouldKeep) pair to the bounded
// agreement/disagreement label. Both disagreement directions are distinct so
// the operator can tell which way a flip would go.
func agreementOutcome(cosineKept, xeWouldKeep bool) string {
	switch {
	case cosineKept && xeWouldKeep:
		return engine.AgreeKept
	case !cosineKept && !xeWouldKeep:
		return engine.AgreeRejected
	case cosineKept && !xeWouldKeep:
		return engine.DisagreeCosineKeptXEReject
	default: // !cosineKept && xeWouldKeep
		return engine.DisagreeCosineRejXEKeeps
	}
}

// classifyCrossEncoderError maps a cross-encoder error to a bounded degraded-
// reason label. timeout → context deadline; else error.
//
// circuit_open was removed: the shadow client is constructed without
// WithCircuit, so ErrCircuitOpen cannot occur. If a future PR wires a circuit
// breaker, it can re-add the label.
//
// context.Canceled is NOT classified as timeout: it means the parent context
// was cancelled (client disconnect), not a deadline. With the detached
// context (context.WithoutCancel) Canceled should not reach the rerank call,
// but if it ever does it falls through to error — correct, not a lie.
// (The embed path's classifyEmbedError has the same Canceled-as-timeout bug;
// filed separately, not fixed in this PR.)
func classifyCrossEncoderError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return engine.CrossEncoderReasonTimeout
	}
	return engine.CrossEncoderReasonError
}
