package search

import (
	"context"

	"github.com/anatolykoptev/go-engine/metrics"
	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-engine/websearch"
)

const metricBingRequests = "bing_requests"

// SearchBingDirect queries Bing Search directly using browser TLS fingerprint.
// Delegates to websearch.Bing.
func SearchBingDirect(ctx context.Context, bc BrowserDoer, query string, m *metrics.Registry) ([]sources.Result, error) {
	if m != nil {
		m.Incr(metricBingRequests)
	}
	b := websearch.NewBing(websearch.WithBingBrowser(bc))
	ws, err := b.Search(ctx, query, websearch.SearchOpts{})
	if err != nil {
		return nil, err
	}
	return ws, nil
}
