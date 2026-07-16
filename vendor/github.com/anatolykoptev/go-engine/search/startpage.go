package search

import (
	"context"

	"github.com/anatolykoptev/go-engine/metrics"
	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-engine/websearch"
)

const metricStartpageRequests = "startpage_requests"

// SearchStartpageDirect queries Startpage directly using browser TLS fingerprint.
// Returns results compatible with the SearXNG pipeline.
// Delegates to websearch.Startpage.
func SearchStartpageDirect(ctx context.Context, bc BrowserDoer, query, language string, m *metrics.Registry, timeRange ...string) ([]sources.Result, error) {
	if m != nil {
		m.Incr(metricStartpageRequests)
	}
	tr := ""
	if len(timeRange) > 0 {
		tr = timeRange[0]
	}
	sp := websearch.NewStartpage(websearch.WithStartpageBrowser(bc))
	ws, err := sp.Search(ctx, query, websearch.SearchOpts{Language: language, TimeRange: tr})
	if err != nil {
		return nil, err
	}
	return ws, nil
}

// ParseStartpageHTML extracts search results from Startpage HTML response.
// Delegates to websearch.ParseStartpageHTML.
func ParseStartpageHTML(data []byte) ([]sources.Result, error) {
	ws, err := websearch.ParseStartpageHTML(data)
	return ws, err
}
