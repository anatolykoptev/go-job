package search

import (
	"context"
	"log/slog"
	"sync"

	"github.com/anatolykoptev/go-engine/fetch"
	"github.com/anatolykoptev/go-engine/metrics"
)

// DirectConfig controls the SearchDirect fan-out behavior.
type DirectConfig struct {
	Browser   BrowserDoer
	DDG       bool
	Startpage bool
	Retry     fetch.RetryConfig
	Metrics   *metrics.Registry
}

// SearchDirect queries enabled direct scrapers in parallel.
// Returns merged results from all direct sources. Failures are non-fatal.
func SearchDirect(ctx context.Context, cfg DirectConfig, query, language string) []Result {
	if cfg.Browser == nil {
		return nil
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var all []Result

	if cfg.DDG {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, err := fetch.RetryDo(ctx, cfg.Retry, func() ([]Result, error) {
				return SearchDDGDirect(ctx, cfg.Browser, query, "wt-wt", cfg.Metrics)
			})
			if err != nil {
				slog.Debug("ddg direct failed", slog.Any("error", err))
				return
			}
			slog.Debug("ddg direct results", slog.Int("count", len(results)))
			mu.Lock()
			all = append(all, results...)
			mu.Unlock()
		}()
	}

	if cfg.Startpage {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, err := fetch.RetryDo(ctx, cfg.Retry, func() ([]Result, error) {
				return SearchStartpageDirect(ctx, cfg.Browser, query, language, cfg.Metrics)
			})
			if err != nil {
				slog.Debug("startpage direct failed", slog.Any("error", err))
				return
			}
			slog.Debug("startpage direct results", slog.Int("count", len(results)))
			mu.Lock()
			all = append(all, results...)
			mu.Unlock()
		}()
	}

	wg.Wait()
	return all
}
