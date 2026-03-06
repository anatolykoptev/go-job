package engine

import (
	"context"

	"github.com/anatolykoptev/go-engine/search"
	"golang.org/x/time/rate"
)

// DefaultSearchEngine is the SearXNG engine used for site: queries.
const DefaultSearchEngine = "bing"

// SearchSearXNG queries the SearXNG instance and returns raw results.
// Returns nil, nil when SearXNG is not configured (searxngInst == nil).
func SearchSearXNG(ctx context.Context, query, language, timeRange, engines string) ([]SearxngResult, error) {
	if searxngInst == nil {
		return nil, nil
	}
	return searxngInst.Search(ctx, query, language, timeRange, engines)
}

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
func SearchDirect(ctx context.Context, query, language string) []SearxngResult {
	return search.SearchDirect(ctx, directSearchConfig(), query, language)
}

// directSearchConfig builds a search.DirectConfig from engine state.
func directSearchConfig() search.DirectConfig {
	return search.DirectConfig{
		Browser:       fetcherProxy.BrowserClient(),
		DDG:           cfg.DirectDDG,
		Startpage:     cfg.DirectStartpage,
		Brave:         cfg.DirectBrave,
		Reddit:        cfg.DirectReddit,
		BraveLimiter:  rate.NewLimiter(1, 2),
		RedditLimiter: rate.NewLimiter(1, 2),
		Retry:         DefaultRetryConfig,
		Metrics:       reg,
	}
}
