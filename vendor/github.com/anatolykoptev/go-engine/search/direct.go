package search

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/time/rate"

	kitmetrics "github.com/anatolykoptev/go-kit/metrics"

	"github.com/anatolykoptev/go-engine/fetch"
	"github.com/anatolykoptev/go-engine/metrics"
	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-engine/websearch"
)

// defaultPerSourceTimeout is the per-source goroutine deadline (retries included)
// used when DirectConfig.PerSourceTimeout is zero.
const defaultPerSourceTimeout = 6 * time.Second

// defaultEarlyReturnAt is the result-count threshold that triggers early
// cancellation when DirectConfig.EarlyReturnAt is zero.
const defaultEarlyReturnAt = 10

// BrowserDoer performs HTTP requests with browser-like TLS fingerprint.
// *stealth.BrowserClient satisfies this interface.
type BrowserDoer interface {
	Do(method, url string, headers map[string]string, body io.Reader) ([]byte, map[string]string, int, error)
}

// DirectConfig controls the SearchDirect fan-out behavior.
type DirectConfig struct {
	Browser         BrowserDoer
	FallbackBrowser BrowserDoer // optional: used when Browser fails on proxy-quota/gateway statuses (402/407/5xx)
	// MojeekBrowser, when non-nil, is the doer used exclusively for Mojeek
	// (set it to a residential-proxy-backed BrowserClient). Mojeek blocks our
	// datacenter egress IP at the network level (lighttpd 403 "automated
	// queries", served before TLS/headers), so the shared direct-primary
	// Browser is permanently blocked for it; the dualBrowser fallback only
	// escalates on 402/407/5xx, not the 403 Mojeek returns. When nil, runMojeek
	// falls back to Browser (default, backward-compatible).
	MojeekBrowser    BrowserDoer
	DDG              bool
	Startpage        bool
	Brave            bool
	Reddit           bool
	Bing             bool
	Yep              bool
	Wikipedia        bool
	Marginalia       bool
	Mojeek           bool
	Yandex           YandexConfig
	Retry            fetch.RetryConfig
	Metrics          *metrics.Registry
	DDGLimiter       *rate.Limiter
	StartpageLimiter *rate.Limiter
	BraveLimiter     *rate.Limiter
	RedditLimiter    *rate.Limiter
	BingLimiter      *rate.Limiter

	// RedditTokenManager, when non-nil, enables Tier 1 OAuth search (Phase 2).
	// Zero value (nil) → inactive; legacy SearchRedditDirect path used.
	RedditTokenManager websearch.RedditTokenManager

	// RedditUserAgent is sent as the User-Agent in Reddit OAuth requests.
	// Only used when RedditTokenManager is non-nil.
	RedditUserAgent string

	// RedditCookieSearch, when non-nil, enables Tier 2 cookie-based search.
	// Receives (ctx, query) and returns (results, error).
	RedditCookieSearch func(ctx context.Context, query string) ([]sources.Result, error)

	// RedditBrowserRender, when non-nil, enables Tier 3 browser-render search.
	// Receives (ctx, query) and returns (results, error).
	RedditBrowserRender func(ctx context.Context, query string) ([]sources.Result, error)

	// PerSourceTimeout caps each source goroutine (retries included).
	// Default defaultPerSourceTimeout (6s) when zero.
	PerSourceTimeout time.Duration

	// EarlyReturnAt is a soft cap: cancels in-flight sources once N results
	// are collected across all sources; already-delivered results are kept,
	// so the final count may exceed N. Trades completeness for lower tail
	// latency: a caller wanting maximum result coverage should set this high
	// and rely on PerSourceTimeout to bound the overall wall-clock time instead.
	// Default defaultEarlyReturnAt (10) when zero.
	EarlyReturnAt int
}

// directResult holds the outcome of one source goroutine.
type directResult struct {
	label   string
	results []sources.Result
	err     error
	dur     time.Duration
}

// directJob describes one enabled scraper in the fan-out.
type directJob struct {
	enabled bool
	label   string
	fn      func(context.Context) ([]sources.Result, error)
}

// runSourceWithTimeout executes fn inside a goroutine that is capped by srcCtx.
// It sends the result to ch once fn completes or srcCtx is cancelled (whichever
// comes first), so a blocked BrowserDoer.Do call cannot exceed the timeout.
//
// Goroutine lifetime note: the inner goroutine that calls fn may outlive srcCtx
// cancellation if the BrowserDoer.Do implementation does not respect context
// cancellation promptly. It is bounded in practice by the HTTP client Timeout
// (~15 s for the stealth client), so it cannot run indefinitely. done MUST remain
// buffered (cap >= 1) so that when runSourceWithTimeout returns on the srcCtx.Done
// branch the inner goroutine can still send without blocking forever.
func runSourceWithTimeout(srcCtx context.Context, label string, fn func(context.Context) ([]sources.Result, error), ch chan<- directResult) {
	start := time.Now()
	type fnResult struct {
		res []sources.Result
		err error
	}
	done := make(chan fnResult, 1)
	go func() {
		res, err := fn(srcCtx)
		done <- fnResult{res, err}
	}()

	var res []sources.Result
	var err error
	select {
	case r := <-done:
		res, err = r.res, r.err
	case <-srcCtx.Done():
		err = srcCtx.Err()
	}
	ch <- directResult{label: label, results: res, err: err, dur: time.Since(start)}
}

// metricSourceResult is the per-source fan-out outcome counter. Encoded as
// name{source=<label>,outcome=ok|empty|fail} so the go-kit/metrics Prometheus bridge
// surfaces it as go_search_source_result_total{source="yep",outcome="fail"}.
//
// Outcomes:
//   - ok    — source returned ≥1 result
//   - empty — source returned HTTP 200 with zero results (silent-block signature:
//     e.g. mojeek 403 masquerading as empty, geo-blocked source)
//   - fail  — source returned an error
//
// Rationale: a source failing 100% (e.g. yep on the deprecated endpoint) was
// invisible because a sibling source (yandex) silently covered the result set.
// This counter makes a per-source failure rate alertable. The "empty" outcome
// additionally surfaces silent blocks where the source appears healthy (no error)
// but consistently returns zero usable results.
const metricSourceResult = "go_search_source_result_total"

// recordSourceResult increments the per-source outcome counter. Nil-safe.
func recordSourceResult(m *metrics.Registry, source, outcome string) {
	if m == nil {
		return
	}
	m.Incr(kitmetrics.Label(metricSourceResult, "source", source, "outcome", outcome))
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
		slog.Bool("wikipedia", cfg.Wikipedia),
		slog.Bool("marginalia", cfg.Marginalia),
		slog.Bool("mojeek", cfg.Mojeek),
		slog.Bool("fallback_browser", cfg.FallbackBrowser != nil),
	)

	cfg.Browser = newDualBrowser(cfg.Browser, cfg.FallbackBrowser)

	perSrc := cfg.PerSourceTimeout
	if perSrc == 0 {
		perSrc = defaultPerSourceTimeout
	}
	earlyAt := cfg.EarlyReturnAt
	if earlyAt == 0 {
		earlyAt = defaultEarlyReturnAt
	}

	// allDoneCtx is cancelled when enough results accumulate (early-return path).
	allDoneCtx, allDoneCancel := context.WithCancel(ctx)
	defer allDoneCancel()

	jobs := []directJob{
		{cfg.DDG, "ddg", func(ctx context.Context) ([]sources.Result, error) { return runDDG(ctx, cfg, query) }},
		{cfg.Startpage, "startpage", func(ctx context.Context) ([]sources.Result, error) {
			return runStartpage(ctx, cfg, query, language)
		}},
		{cfg.Brave, "brave", func(ctx context.Context) ([]sources.Result, error) { return runBrave(ctx, cfg, query) }},
		{cfg.Reddit, "reddit", func(ctx context.Context) ([]sources.Result, error) { return runReddit(ctx, cfg, query) }},
		{cfg.Bing, "bing", func(ctx context.Context) ([]sources.Result, error) { return runBing(ctx, cfg, query) }},
		{cfg.Yep, "yep", func(ctx context.Context) ([]sources.Result, error) {
			y := websearch.NewYep(websearch.WithYepBrowser(cfg.Browser))
			return y.Search(ctx, query, websearch.SearchOpts{})
		}},
		{cfg.Yandex.APIKey != "", "yandex", func(ctx context.Context) ([]sources.Result, error) {
			return SearchYandexAPI(ctx, cfg.Yandex, query, "", cfg.Metrics)
		}},
		{cfg.Wikipedia, "wikipedia", func(ctx context.Context) ([]sources.Result, error) {
			return runWikipedia(ctx, cfg, query, language)
		}},
		{cfg.Marginalia, "marginalia", func(ctx context.Context) ([]sources.Result, error) {
			return runMarginalia(ctx, cfg, query)
		}},
		{cfg.Mojeek, "mojeek", func(ctx context.Context) ([]sources.Result, error) {
			return runMojeek(ctx, cfg, query)
		}},
	}

	enabled := 0
	for _, j := range jobs {
		if j.enabled {
			enabled++
		}
	}
	if enabled == 0 {
		return nil
	}

	ch := make(chan directResult, enabled)
	var wg sync.WaitGroup
	for _, j := range jobs {
		if !j.enabled {
			continue
		}
		wg.Add(1)
		go func(label string, fn func(context.Context) ([]sources.Result, error)) {
			defer wg.Done()
			srcCtx, srcCancel := context.WithTimeout(allDoneCtx, perSrc)
			defer srcCancel()
			runSourceWithTimeout(srcCtx, label, fn, ch)
		}(j.label, j.fn)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	return collectResults(ch, cfg.Metrics, earlyAt, allDoneCancel)
}

// collectResults drains ch and accumulates results, emitting metrics and triggering
// early-return cancellation once earlyAt results are collected.
func collectResults(ch <-chan directResult, m *metrics.Registry, earlyAt int, cancel context.CancelFunc) []sources.Result {
	var all []sources.Result
	var cancelled bool
	for r := range ch {
		if m != nil {
			m.ObserveSeconds(
				kitmetrics.Label("go_search_search_source_duration_seconds", "source", r.label),
				r.dur,
			)
		}
		if r.err != nil {
			recordSourceResult(m, r.label, "fail")
			slog.Warn("search source failed", slog.String("source", r.label), slog.Any("error", r.err))
			continue
		}
		if len(r.results) == 0 {
			recordSourceResult(m, r.label, "empty")
			slog.Info("search source empty", slog.String("source", r.label))
			continue
		}
		recordSourceResult(m, r.label, "ok")
		slog.Info("search source results", slog.String("source", r.label), slog.Int("count", len(r.results)))
		all = append(all, r.results...)
		if !cancelled && len(all) >= earlyAt {
			cancel()
			cancelled = true
		}
	}
	return all
}
