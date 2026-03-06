package search

import (
	"context"

	"github.com/anatolykoptev/go-engine/metrics"
	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-stealth/websearch"
)

const metricStartpageRequests = "startpage_requests"

// SearchStartpageDirect queries Startpage directly using browser TLS fingerprint.
// Returns results compatible with the SearXNG pipeline.
// Delegates to websearch.Startpage.
func SearchStartpageDirect(ctx context.Context, bc BrowserDoer, query, language string, m *metrics.Registry) ([]sources.Result, error) {
	if m != nil {
		m.Incr(metricStartpageRequests)
	}
	sp := websearch.NewStartpage(websearch.WithStartpageBrowser(bc))
	ws, err := sp.Search(ctx, query, websearch.SearchOpts{Language: language})
	if err != nil {
		return nil, err
	}
	return wsToSourceResults(ws), nil
}

// ParseStartpageHTML extracts search results from Startpage HTML response.
// Delegates to websearch.ParseStartpageHTML.
func ParseStartpageHTML(data []byte) ([]sources.Result, error) {
	ws, err := websearch.ParseStartpageHTML(data)
	return wsToSourceResults(ws), err
}
