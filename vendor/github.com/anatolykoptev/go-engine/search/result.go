// Package search provides web search aggregation across multiple engines.
//
// Includes a [SearXNG] client, [SearchDDGDirect] and [SearchStartpageDirect]
// direct scrapers, and utility functions [FilterByScore] and [DedupByDomain].
package search

import "io"

// Result represents a single search result from any search engine.
type Result struct {
	Title   string  `json:"title"`
	Content string  `json:"content"`
	URL     string  `json:"url"`
	Score   float64 `json:"score"`
}

// searxngResponse is the JSON response from SearXNG API.
type searxngResponse struct {
	Results []Result `json:"results"`
}

// BrowserDoer performs HTTP requests with browser-like TLS fingerprint.
// *stealth.BrowserClient satisfies this interface.
type BrowserDoer interface {
	Do(method, url string, headers map[string]string, body io.Reader) ([]byte, map[string]string, int, error)
}
