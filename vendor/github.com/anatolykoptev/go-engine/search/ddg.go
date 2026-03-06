package search

import (
	"context"

	"github.com/anatolykoptev/go-engine/metrics"
	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-stealth/websearch"
)

const metricDDGRequests = "ddg_requests"

// SearchDDGDirect queries DuckDuckGo directly using browser TLS fingerprint.
// Uses the HTML lite endpoint as primary, falls back to d.js JSON API.
// Delegates to websearch.DDG.
func SearchDDGDirect(ctx context.Context, bc BrowserDoer, query, region string, m *metrics.Registry) ([]sources.Result, error) {
	if m != nil {
		m.Incr(metricDDGRequests)
	}
	opts := []websearch.DDGOption{websearch.WithDDGBrowser(bc)}
	if region != "" {
		opts = append(opts, websearch.WithDDGRegion(region))
	}
	ddg, err := websearch.NewDDG(opts...)
	if err != nil {
		return nil, err
	}
	ws, err := ddg.Search(ctx, query, websearch.SearchOpts{})
	if err != nil {
		return nil, err
	}
	return wsToSourceResults(ws), nil
}

// ParseDDGHTML extracts search results from DDG HTML lite response.
// Delegates to websearch.ParseDDGHTML.
func ParseDDGHTML(data []byte) ([]sources.Result, error) {
	ws, err := websearch.ParseDDGHTML(data)
	return wsToSourceResults(ws), err
}

// DDGUnwrapURL extracts the actual URL from DDG redirect wrappers.
// Delegates to websearch.DDGUnwrapURL.
func DDGUnwrapURL(href string) string {
	return websearch.DDGUnwrapURL(href)
}

// ParseDDGResponse extracts search results from DDG d.js response.
// Delegates to websearch.ParseDDGResponse.
func ParseDDGResponse(data []byte) ([]sources.Result, error) {
	ws, err := websearch.ParseDDGResponse(data)
	return wsToSourceResults(ws), err
}

// ExtractVQD extracts the VQD token from DDG response HTML.
// Delegates to websearch.ExtractVQD.
func ExtractVQD(body string) string {
	return websearch.ExtractVQD(body)
}
