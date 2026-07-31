package jobserver

import (
	"context"
	"errors"
	"log/slog"

	"github.com/anatolykoptev/go-engine/websearch"
	kitembed "github.com/anatolykoptev/go-kit/embed"
	"github.com/anatolykoptev/go-kit/env"
	"github.com/anatolykoptev/go-kit/rerank"
	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
)

// Relevance gate configuration (env-tunable).
//
// jobSearchMinRelevance is the cosine-similarity threshold below which a
// candidate is rejected. Default 0.80 — measured against the live embedder
// (embed.krolik.tools, e5-large) on the two reference queries from
// ~/tmp/relevance_before.txt: off-topic titles scored 0.7502–0.7935, on-topic
// titles scored 0.8269–0.8822. 0.80 sits in the gap.
//
// jobSearchMinKeep is the floor: if fewer than this many candidates pass the
// threshold, the top-N by score are kept anyway (prevents an empty result set
// when the embedder is healthy but all candidates are marginal). Default 3.
var (
	jobSearchMinRelevance = env.Float("JOB_SEARCH_MIN_RELEVANCE", 0.80)
	jobSearchMinKeep      = env.Int("JOB_SEARCH_MIN_KEEP", 3)
)

// maxSnippetRunes bounds the snippet length embedded per candidate. Full page
// text is not embedded — only title + company (from title) + a snippet.
const maxSnippetRunes = 500

// applyRelevanceGate scores every candidate against the query via cosine
// similarity (embedder + rerank.MathReranker with Lambda=0 for pure cosine
// sort), writes the score into each result's Score field, and filters by
// websearch.FilterByScore.
//
// Fail-open contract: if the embedder is not configured, returns an error,
// times out, has its circuit breaker open, or returns empty/mismatched
// vectors, the input results are returned UNFILTERED (in their original order)
// and a non-empty degraded reason string is returned. The caller makes the
// degradation visible in the output summary and bumps the
// job_search_relevance_degraded_total{reason} metric.
//
// Returns (filteredResults, degradedReason). degradedReason is "" when the
// gate ran successfully (even if it kept everything via the min-keep floor).
func applyRelevanceGate(ctx context.Context, query string, results []engine.SearxngResult) ([]engine.SearxngResult, string) {
	if len(results) == 0 {
		return results, ""
	}

	ec := jobs.GetEmbedClient()
	if ec == nil {
		engine.IncrJobSearchRelevanceDegraded(engine.RelevanceReasonNotConfigured)
		slog.Info("job_search: relevance gate skipped — embedder not configured")
		return results, engine.RelevanceReasonNotConfigured
	}

	// Build document strings: title + bounded snippet.
	docs := make([]rerank.Doc, len(results))
	passages := make([]string, len(results))
	for i, r := range results {
		doc := r.Title
		if r.Content != "" {
			snippet := truncateRunes(r.Content, maxSnippetRunes)
			doc = doc + " " + snippet
		}
		passages[i] = "passage: " + doc
		docs[i] = rerank.Doc{ID: r.URL, Text: doc}
	}

	// Embed query once.
	qvec, err := ec.EmbedQuery(ctx, "query: "+query)
	if err != nil {
		reason := classifyEmbedError(err)
		engine.IncrJobSearchRelevanceDegraded(reason)
		slog.Warn("job_search: relevance gate degraded — embed query failed",
			slog.Any("error", err),
			slog.String("reason", reason))
		return results, reason
	}
	if len(qvec) == 0 {
		engine.IncrJobSearchRelevanceDegraded(engine.RelevanceReasonEmptyVectors)
		slog.Warn("job_search: relevance gate degraded — empty query vector")
		return results, engine.RelevanceReasonEmptyVectors
	}

	// Embed all passages in one batch.
	vecs, err := ec.Embed(ctx, passages)
	if err != nil {
		reason := classifyEmbedError(err)
		engine.IncrJobSearchRelevanceDegraded(reason)
		slog.Warn("job_search: relevance gate degraded — embed passages failed",
			slog.Any("error", err),
			slog.String("reason", reason))
		return results, reason
	}
	if len(vecs) < len(results) {
		engine.IncrJobSearchRelevanceDegraded(engine.RelevanceReasonEmptyVectors)
		slog.Warn("job_search: relevance gate degraded — partial embeddings",
			slog.Int("got", len(vecs)),
			slog.Int("want", len(results)))
		return results, engine.RelevanceReasonEmptyVectors
	}

	// Attach vectors to docs.
	for i := range docs {
		if i < len(vecs) && len(vecs[i]) > 0 {
			docs[i].EmbedVector = vecs[i]
		}
	}

	// Score with MathReranker (Lambda=0 → pure cosine sort).
	rr := rerank.MathReranker{QueryVector: qvec, Lambda: 0}
	scored := rr.Rerank(ctx, query, docs)

	// Write cosine into each result's Score field, in scored (desc) order.
	sorted := make([]engine.SearxngResult, len(scored))
	for i, s := range scored {
		idx := s.OrigRank
		if idx >= 0 && idx < len(results) {
			results[idx].Score = float64(s.Score)
		}
		sorted[i] = results[idx]
	}

	// Count scored.
	for range sorted {
		engine.IncrJobSearchRelevance(engine.RelevanceScored)
	}

	// Gate with FilterByScore (keeps at least minKeep).
	filtered := websearch.FilterByScore(sorted, jobSearchMinRelevance, jobSearchMinKeep)

	for range filtered {
		engine.IncrJobSearchRelevance(engine.RelevanceKept)
	}
	for i := range sorted {
		kept := false
		for _, f := range filtered {
			if f.URL == sorted[i].URL && f.Score == sorted[i].Score {
				kept = true
				break
			}
		}
		if !kept {
			engine.IncrJobSearchRelevance(engine.RelevanceRejected)
		}
	}

	slog.Info("job_search: relevance gate applied",
		slog.Int("candidates", len(sorted)),
		slog.Int("kept", len(filtered)),
		slog.Float64("min_relevance", jobSearchMinRelevance),
		slog.Int("min_keep", jobSearchMinKeep))

	return filtered, ""
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

// truncateRunes returns the first n runes of s, with no suffix.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
