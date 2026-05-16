package websearch

import (
	"context"
	"errors"
	"fmt"
)

// Fallback tries providers in order, returning the first successful result.
type Fallback struct {
	providers []Provider
}

// NewFallback creates a fallback provider chain.
// The first provider is primary; subsequent providers are tried on error.
func NewFallback(providers ...Provider) *Fallback {
	return &Fallback{providers: providers}
}

// Search tries each provider in order until one succeeds.
func (f *Fallback) Search(ctx context.Context, query string, opts SearchOpts) ([]Result, error) {
	var errs []error
	for _, p := range f.providers {
		results, err := p.Search(ctx, query, opts)
		if err == nil {
			return results, nil
		}
		errs = append(errs, err)
	}
	return nil, fmt.Errorf("all providers failed: %w", errors.Join(errs...))
}
