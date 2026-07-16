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
func SearchBingDirect(ctx context.Context, bc BrowserDoer, query string, m *metrics.Registry, timeRange ...string) ([]sources.Result, error) {
	if m != nil {
		m.Incr(metricBingRequests)
	}
	tr := ""
	if len(timeRange) > 0 {
		tr = timeRange[0]
	}
	b := websearch.NewBing(websearch.WithBingBrowser(bc))
	ws, err := b.Search(ctx, query, websearch.SearchOpts{TimeRange: tr})
	if err != nil {
		return nil, err
	}
	return ws, nil
}
