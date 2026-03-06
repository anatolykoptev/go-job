package pipeline

import (
	"context"
	"fmt"
	"sync"
)

// FetchResult holds the outcome for a single URL fetch.
type FetchResult struct {
	URL     string
	Content string
	Err     error
}

// FetchOption configures ParallelFetch behavior.
type FetchOption func(*fetchConfig)

type fetchConfig struct {
	maxConcurrency int
}

// WithMaxConcurrency limits the number of concurrent fetch goroutines.
// Default is 10.
func WithMaxConcurrency(n int) FetchOption {
	return func(c *fetchConfig) {
		if n > 0 {
			c.maxConcurrency = n
		}
	}
}

// ParallelFetch fetches URL content with bounded concurrency.
// Returns a result for every URL including errors (never silently skips).
// Results are in the same order as the input URLs.
func ParallelFetch(ctx context.Context, urls []string,
	fetchFn func(ctx context.Context, url string) (string, error),
	opts ...FetchOption) []FetchResult {

	if len(urls) == 0 {
		return nil
	}

	cfg := fetchConfig{maxConcurrency: 10}
	for _, o := range opts {
		o(&cfg)
	}

	results := make([]FetchResult, len(urls))
	sem := make(chan struct{}, cfg.maxConcurrency)
	var wg sync.WaitGroup

	for i, u := range urls {
		wg.Add(1)
		go func(idx int, url string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					results[idx] = FetchResult{URL: url, Err: fmt.Errorf("panic: %v", r)}
				}
			}()
			content, err := fetchFn(ctx, url)
			results[idx] = FetchResult{URL: url, Content: content, Err: err}
		}(i, u)
	}

	wg.Wait()
	return results
}
