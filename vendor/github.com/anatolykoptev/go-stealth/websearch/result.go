package websearch

import "context"

// Result is a single search result from any engine.
type Result struct {
	Title    string
	URL      string
	Content  string
	Score    float64
	Metadata map[string]string
}

// SearchOpts are optional search parameters.
type SearchOpts struct {
	Language  string // ISO 639-1 (e.g. "en", "ru")
	TimeRange string // "", "day", "week", "month", "year"
	Region    string // engine-specific region code
	Engines   string // comma-separated engine list (SearXNG)
	Limit     int    // max results (0 = engine default)
}

// Provider searches the web and returns results.
type Provider interface {
	Search(ctx context.Context, query string, opts SearchOpts) ([]Result, error)
}
