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
func SearchBraveDirect(ctx context.Context, bc BrowserDoer, query string, m *metrics.Registry) ([]sources.Result, error) {
	if m != nil {
		m.Incr(metricBraveRequests)
	}
	b := websearch.NewBrave(websearch.WithBraveBrowser(bc))
	ws, err := b.Search(ctx, query, websearch.SearchOpts{})
	if err != nil {
		return nil, err
	}
	return ws, nil
}
