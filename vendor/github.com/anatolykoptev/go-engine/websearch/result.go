package websearch

import (
	"context"

	"github.com/anatolykoptev/go-engine/sources"
)

// Result is a type alias for sources.Result — eliminates conversion overhead.
type Result = sources.Result

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
