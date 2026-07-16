package search

import (
	"context"

	"github.com/anatolykoptev/go-engine/metrics"
	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-engine/websearch"
)

const metricBraveRequests = "brave_requests"

// SearchBraveDirect queries Brave Search directly using browser TLS fingerprint.
// Delegates to websearch.Brave.
func SearchBraveDirect(ctx context.Context, bc BrowserDoer, query string, m *metrics.Registry, timeRange ...string) ([]sources.Result, error) {
	if m != nil {
		m.Incr(metricBraveRequests)
	}
	tr := ""
	if len(timeRange) > 0 {
		tr = timeRange[0]
	}
	b := websearch.NewBrave(websearch.WithBraveBrowser(bc))
	ws, err := b.Search(ctx, query, websearch.SearchOpts{TimeRange: tr})
	if err != nil {
		return nil, err
	}
	return ws, nil
}
