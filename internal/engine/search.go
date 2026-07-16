package engine

import (
	"context"

	"github.com/anatolykoptev/go-engine/search"
	"golang.org/x/time/rate"
)

// FilterByScore removes results below minScore, keeping at least minKeep.
func FilterByScore(results []SearxngResult, minScore float64, minKeep int) []SearxngResult {
	return search.FilterByScore(results, minScore, minKeep)
}

// DedupByDomain limits results to maxPerDomain per domain.
func DedupByDomain(results []SearxngResult, maxPerDomain int) []SearxngResult {
	return search.DedupByDomain(results, maxPerDomain)
}

// SearchDirect queries enabled direct scrapers in parallel.
// Returns merged results from all direct sources. Failures are non-fatal.
// Returns nil when the engine is not initialized (fetcherProxy == nil).
//
// Discards per-leg DirectStats — use SearchDirectWithStats if you need
// the degraded-mode signal (Attempted > 0 && OK == 0).
func SearchDirect(ctx context.Context, query, language string) []SearxngResult {
	results, _ := SearchDirectWithStats(ctx, query, language)
	return results
}

// SearchDirectWithStats is like SearchDirect but also returns DirectStats
// from the upstream fan-out. The primary signal: Attempted > 0 && OK == 0
// means every launched leg was blocked or failed (DC-IP / censorship
// degraded mode), distinguishable from genuine zero results.
func SearchDirectWithStats(ctx context.Context, query, language string) ([]SearxngResult, search.DirectStats) {
	if fetcherProxy == nil {
		return nil, search.DirectStats{}
	}
	results, stats := search.SearchDirect(ctx, directSearchConfig(), query, language)
	return results, stats
}

// directBrowser returns the best available BrowserDoer for direct scrapers.
// Prefers DirectClient (no-proxy Chrome-TLS, built when FETCH_DIRECT_FIRST is set)
// and falls back to BrowserClient (proxy-backed). Returns nil when neither is
// available, which causes SearchDirect to log "browser nil" and return empty.
func directBrowser() search.BrowserDoer {
	if dc := fetcherProxy.DirectClient(); dc != nil {
		return dc
	}
	return fetcherProxy.BrowserClient()
}

// directSearchConfig builds a search.DirectConfig from engine state.
func directSearchConfig() search.DirectConfig {
	return search.DirectConfig{
		Browser:          directBrowser(),
		DDG:              cfg.DirectDDG,
		Startpage:        cfg.DirectStartpage,
		Brave:            cfg.DirectBrave,
		Reddit:           cfg.DirectReddit,
		Wikipedia:        cfg.DirectWikipedia,
		Marginalia:       cfg.DirectMarginalia,
		BraveLimiter:     rate.NewLimiter(1, 2),
		RedditLimiter:    rate.NewLimiter(1, 2),
		Retry:            DefaultRetryConfig,
		Metrics:          reg,
		EarlyReturnAt:    cfg.SearchEarlyReturnAt,
		PerSourceTimeout: cfg.SearchPerSourceTimeout,
	}
}
