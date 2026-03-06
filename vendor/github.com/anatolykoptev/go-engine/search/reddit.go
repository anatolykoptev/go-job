package search

import (
	"context"

	"github.com/anatolykoptev/go-engine/metrics"
	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-stealth/websearch"
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
	return wsToSourceResults(ws), nil
}
