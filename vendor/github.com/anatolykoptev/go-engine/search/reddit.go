package search

import (
	"context"
	"errors"

	"github.com/anatolykoptev/go-engine/metrics"
	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-engine/websearch"
)

const metricRedditRequests = "reddit_requests"

// SearchRedditDirect queries Reddit JSON API using browser TLS fingerprint.
// Delegates to websearch.Reddit.
func SearchRedditDirect(ctx context.Context, bc BrowserDoer, query string, m *metrics.Registry) ([]sources.Result, error) {
	if m != nil {
		m.Incr(metricRedditRequests)
	}
	r := websearch.NewReddit(websearch.WithRedditBrowser(bc))
	ws, err := r.Search(ctx, query, websearch.SearchOpts{Limit: 10})
	if err != nil {
		return nil, err
	}
	return ws, nil
}

// metricRedditTier is the per-tier outcome counter for the Reddit escalation chain.
// Counter name: go_search_reddit_tier_total{tier=<tier>,outcome=<outcome>}
//
// Outcomes:
//   - empty        — tier returned 0 results with no error (escalating to next tier)
//   - rate_limited — tier returned *ErrRateLimited
//   - error        — tier returned any other non-nil error
//
// Note: success short-circuits before recordTierOutcome is called, so "ok"
// is never emitted as an outcome label.
const metricRedditTier = "go_search_reddit_tier_total"

// tierOutcomeLabel maps a tier exit error to a bounded outcome label for the
// reddit_tier counter. nil means "tier produced zero results" (empty outcome).
func tierOutcomeLabel(err error) string {
	if err == nil {
		return "empty"
	}
	var rl *ErrRateLimited
	if errors.As(err, &rl) {
		return "rate_limited"
	}
	return "error"
}

// recordTierOutcome computes the outcome label for the given tier exit error and
// records it in the taxonomy. Currently a no-op for metric emission: the registry
// is not threaded through to this call site yet. The live counter
// (go_search_reddit_tier_total) will be wired in the go-search phase when the
// registry is in scope.
//
// err == nil means the tier returned 0 results (empty outcome).
// err is classified into "rate_limited" or "error" via tierOutcomeLabel.
func recordTierOutcome(tier string, err error) {
	// counter name: metricRedditTier, wired in the go-search phase when the registry is in scope
	_ = metricRedditTier
	_, _ = tier, tierOutcomeLabel(err)
}
