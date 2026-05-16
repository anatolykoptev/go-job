package search

import (
	"context"
	"io"
	"log/slog"
	"sync"

	"golang.org/x/time/rate"

	"github.com/anatolykoptev/go-engine/fetch"
	"github.com/anatolykoptev/go-engine/metrics"
	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-engine/websearch"
)

// BrowserDoer performs HTTP requests with browser-like TLS fingerprint.
// *stealth.BrowserClient satisfies this interface.
type BrowserDoer interface {
	Do(method, url string, headers map[string]string, body io.Reader) ([]byte, map[string]string, int, error)
}

// DirectConfig controls the SearchDirect fan-out behavior.
type DirectConfig struct {
	Browser          BrowserDoer
	FallbackBrowser  BrowserDoer // optional: used when Browser fails on proxy-quota/gateway statuses (402/407/5xx)
	DDG              bool
	Startpage        bool
	Brave            bool
	Reddit           bool
	Bing             bool
	Yep              bool
	Yandex           YandexConfig
	Retry            fetch.RetryConfig
	Metrics          *metrics.Registry
	DDGLimiter       *rate.Limiter
	StartpageLimiter *rate.Limiter
	BraveLimiter     *rate.Limiter
	RedditLimiter    *rate.Limiter
	BingLimiter      *rate.Limiter
}

// SearchDirect queries enabled direct scrapers in parallel.
// Returns merged results from all direct sources. Failures are non-fatal.
func SearchDirect(ctx context.Context, cfg DirectConfig, query, language string) []sources.Result {
	if cfg.Browser == nil {
		slog.Info("search direct: browser nil, skipping all scrapers")
		return nil
	}
	slog.Info("search direct: starting",
		slog.Bool("ddg", cfg.DDG),
		slog.Bool("startpage", cfg.Startpage),
		slog.Bool("brave", cfg.Brave),
		slog.Bool("reddit", cfg.Reddit),
		slog.Bool("bing", cfg.Bing),
		slog.Bool("yep", cfg.Yep),
		slog.Bool("yandex", cfg.Yandex.APIKey != ""),
		slog.Bool("fallback_browser", cfg.FallbackBrowser != nil),
	)

	cfg.Browser = newDualBrowser(cfg.Browser, cfg.FallbackBrowser)

	var wg sync.WaitGroup
	var mu sync.Mutex
	var all []sources.Result

	collect := func(results []sources.Result, err error, label string) {
		if err != nil {
			slog.Warn("search source failed", slog.String("source", label), slog.Any("error", err))
			return
		}
		slog.Info("search source results", slog.String("source", label), slog.Int("count", len(results)))
		mu.Lock()
		all = append(all, results...)
		mu.Unlock()
	}

	type job struct {
		enabled bool
		label   string
		fn      func() ([]sources.Result, error)
	}

	jobs := []job{
		{cfg.DDG, "ddg", func() ([]sources.Result, error) { return runDDG(ctx, cfg, query) }},
		{cfg.Startpage, "startpage", func() ([]sources.Result, error) { return runStartpage(ctx, cfg, query, language) }},
		{cfg.Brave, "brave", func() ([]sources.Result, error) { return runBrave(ctx, cfg, query) }},
		{cfg.Reddit, "reddit", func() ([]sources.Result, error) { return runReddit(ctx, cfg, query) }},
		{cfg.Bing, "bing", func() ([]sources.Result, error) { return runBing(ctx, cfg, query) }},
		{cfg.Yep, "yep", func() ([]sources.Result, error) {
			y := websearch.NewYep(websearch.WithYepBrowser(cfg.Browser))
			return y.Search(ctx, query, websearch.SearchOpts{})
		}},
		{cfg.Yandex.APIKey != "", "yandex", func() ([]sources.Result, error) {
			return SearchYandexAPI(ctx, cfg.Yandex, query, "", cfg.Metrics)
		}},
	}

	for _, j := range jobs {
		if !j.enabled {
			continue
		}
		wg.Add(1)
		go func(label string, fn func() ([]sources.Result, error)) {
			defer wg.Done()
			results, err := fn()
			collect(results, err, label)
		}(j.label, j.fn)
	}

	wg.Wait()
	return all
}
