package websearch

import (
	"context"
	"log/slog"
	"sync"

	"golang.org/x/time/rate"
)

// DirectConfig controls the SearchDirect fan-out behavior.
type DirectConfig struct {
	Browser          BrowserDoer
	DDG              bool
	Startpage        bool
	Brave            bool
	Reddit           bool
	Bing             bool
	Yep              bool
	Yandex           YandexConfig
	DDGLimiter       *rate.Limiter
	StartpageLimiter *rate.Limiter
	BraveLimiter     *rate.Limiter
	RedditLimiter    *rate.Limiter
	BingLimiter      *rate.Limiter
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

	collect := func(results []Result, err error, label string) {
		if err != nil {
			slog.Debug(label+" direct failed", slog.Any("error", err))
			return
		}
		slog.Debug(label+" direct results", slog.Int("count", len(results)))
		mu.Lock()
		all = append(all, results...)
		mu.Unlock()
	}

	if cfg.DDG {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, err := runDDGDirect(ctx, cfg, query)
			collect(results, err, "ddg")
		}()
	}

	if cfg.Startpage {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, err := runStartpageDirect(ctx, cfg, query, language)
			collect(results, err, "startpage")
		}()
	}

	if cfg.Brave {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, err := runBraveDirect(ctx, cfg, query)
			collect(results, err, "brave")
		}()
	}

	if cfg.Reddit {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, err := runRedditDirect(ctx, cfg, query)
			collect(results, err, "reddit")
		}()
	}

	if cfg.Bing {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, err := runBingDirect(ctx, cfg, query)
			collect(results, err, "bing")
		}()
	}

	if cfg.Yep {
		wg.Add(1)
		go func() {
			defer wg.Done()
			y := NewYep(WithYepBrowser(cfg.Browser))
			results, err := y.Search(ctx, query, SearchOpts{})
			collect(results, err, "yep")
		}()
	}

	if cfg.Yandex.APIKey != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, err := SearchYandexAPI(ctx, cfg.Yandex, query, "")
			collect(results, err, "yandex")
		}()
	}

	wg.Wait()
	return all
}

// runDDGDirect waits on the optional rate limiter then fetches DDG results.
func runDDGDirect(ctx context.Context, cfg DirectConfig, query string) ([]Result, error) {
	if cfg.DDGLimiter != nil {
		if err := cfg.DDGLimiter.Wait(ctx); err != nil {
			slog.Debug("ddg rate limit wait", slog.Any("error", err))
			return nil, nil //nolint:nilerr // limiter cancelled: skip engine
		}
	}
	ddg, err := NewDDG(WithDDGBrowser(cfg.Browser))
	if err != nil {
		return nil, err
	}
	return ddg.Search(ctx, query, SearchOpts{})
}

// runStartpageDirect waits on the optional rate limiter then fetches Startpage results.
func runStartpageDirect(ctx context.Context, cfg DirectConfig, query, language string) ([]Result, error) {
	if cfg.StartpageLimiter != nil {
		if err := cfg.StartpageLimiter.Wait(ctx); err != nil {
			slog.Debug("startpage rate limit wait", slog.Any("error", err))
			return nil, nil //nolint:nilerr // limiter cancelled: skip engine
		}
	}
	sp := NewStartpage(WithStartpageBrowser(cfg.Browser))
	return sp.Search(ctx, query, SearchOpts{Language: language})
}

// runBraveDirect waits on the optional rate limiter then fetches Brave results.
func runBraveDirect(ctx context.Context, cfg DirectConfig, query string) ([]Result, error) {
	if cfg.BraveLimiter != nil {
		if err := cfg.BraveLimiter.Wait(ctx); err != nil {
			slog.Debug("brave rate limit wait", slog.Any("error", err))
			return nil, nil //nolint:nilerr // limiter cancelled: skip engine
		}
	}
	b := NewBrave(WithBraveBrowser(cfg.Browser))
	return b.Search(ctx, query, SearchOpts{})
}

// runBingDirect waits on the optional rate limiter then fetches Bing results.
func runBingDirect(ctx context.Context, cfg DirectConfig, query string) ([]Result, error) {
	if cfg.BingLimiter != nil {
		if err := cfg.BingLimiter.Wait(ctx); err != nil {
			slog.Debug("bing rate limit wait", slog.Any("error", err))
			return nil, nil //nolint:nilerr // limiter cancelled: skip engine
		}
	}
	b := NewBing(WithBingBrowser(cfg.Browser))
	return b.Search(ctx, query, SearchOpts{})
}

// runRedditDirect waits on the optional rate limiter then fetches Reddit results.
func runRedditDirect(ctx context.Context, cfg DirectConfig, query string) ([]Result, error) {
	if cfg.RedditLimiter != nil {
		if err := cfg.RedditLimiter.Wait(ctx); err != nil {
			slog.Debug("reddit rate limit wait", slog.Any("error", err))
			return nil, nil //nolint:nilerr // limiter cancelled: skip engine
		}
	}
	r := NewReddit(WithRedditBrowser(cfg.Browser))
	return r.Search(ctx, query, SearchOpts{Limit: 10})
}
