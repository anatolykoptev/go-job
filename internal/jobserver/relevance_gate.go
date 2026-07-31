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
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
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
// the tool context. The kit embed client chunks at 32 and issues SEQUENTIAL
// sub-batches at a 30s HTTP timeout each, so a large candidate set can burn
// the whole remaining tool budget; the failure then surfaces one stage later
// as "LLM summarization failed", blaming the wrong component (M3).
var (
	jobSearchMinRelevance     = env.Float("JOB_SEARCH_MIN_RELEVANCE", 0.0)
	jobSearchMinKeep          = env.Int("JOB_SEARCH_MIN_KEEP", 0)
	jobSearchRelevanceTimeout = env.Duration("JOB_SEARCH_RELEVANCE_TIMEOUT", 15*time.Second)
)

func init() {
	validateRelevanceConfig()
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
// scoring. deduped is pre-limit and can carry up to 18 connectors' worth; the
// kit embed client chunks at 32 and issues SEQUENTIAL sub-batches at a 30s
// HTTP timeout each, so an unbounded set can burn the whole tool budget (M3).
// 50 aligns with the max job_search limit — scoring more than the user can
// receive is wasted work. When the cap trims, that is a visible degraded state
// (truncated reason + a caller notice), not silence.
const maxRelevanceCandidates = 50

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

	ec := jobs.GetEmbedClient()
	if ec == nil {
		engine.IncrJobSearchRelevanceDegraded(engine.RelevanceReasonNotConfigured)
		slog.Info("job_search: relevance gate skipped — embedder not configured")
		return results, engine.RelevanceReasonNotConfigured, ""
	}

	// M3: own short timeout so the gate cannot burn the whole tool budget.
	// The kit client chunks at 32 and issues sequential sub-batches at a 30s
	// HTTP timeout each; without this a large candidate set surfaces as a
	// late "LLM summarization failed" blaming the wrong component.
	gateCtx, cancel := context.WithTimeout(ctx, jobSearchRelevanceTimeout)
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
		reason := classifyEmbedError(err)
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
		reason := classifyEmbedError(err)
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
	sorted := make([]engine.SearxngResult, len(scored))
	for i, s := range scored {
		idx := s.OrigRank
		if idx >= 0 && idx < len(candidates) {
			candidates[idx].Score = float64(s.Score)
		}
		sorted[i] = candidates[idx]
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

	// Did the floor engage? FilterByScore returns results[:minKeep] when
	// len(passing) < minKeep && len(sorted) >= minKeep. The survivors beyond
	// the passing count are floor_kept (B2).
	floorEngaged := jobSearchMinKeep > 0 && passingCount < jobSearchMinKeep && len(sorted) >= jobSearchMinKeep && len(filtered) > passingCount
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

	slog.Info("job_search: relevance gate applied",
		slog.Int("candidates", len(sorted)),
		slog.Int("kept", passingCount),
		slog.Int("floor_kept", floorKeptCount),
		slog.Int("rejected", rejectedCount),
		slog.Float64("min_relevance", jobSearchMinRelevance),
		slog.Int("min_keep", jobSearchMinKeep))

	return filtered, "", notice
}

// classifyEmbedError maps an embedder error to a bounded degraded-reason label.
// circuit_open → embed.ErrCircuitOpen; timeout → context deadline; else embed_error.
func classifyEmbedError(err error) string {
	if errors.Is(err, kitembed.ErrCircuitOpen) {
		return engine.RelevanceReasonCircuitOpen
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return engine.RelevanceReasonTimeout
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
