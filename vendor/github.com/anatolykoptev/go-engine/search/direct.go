//nolint:goconst
package search

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"time"

	"golang.org/x/time/rate"

	kitmetrics "github.com/anatolykoptev/go-kit/metrics"
	"github.com/anatolykoptev/go-stealth/ratelimit"

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

// errPerSourceTimeout is the context cause injected by SearchDirect into each
// per-source WithTimeoutCause context. It lets runSourceWithTimeout and
// handleSourceError distinguish a genuine 6 s per-source deadline from an
// inherited parent cancellation (which also surfaces as DeadlineExceeded when
// the caller's ctx has a short budget). Only errPerSourceTimeout triggers
// outcome=timeout and Mark; an inherited deadline falls to outcome=fail so a
// short-budget caller does not pin every in-flight engine for 10 m.
var errPerSourceTimeout = errors.New("per-source timeout")

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

	// TimeRange is an optional recency filter forwarded to the engines that
	// support it (e.g. Bing freshness, DDG df, Brave tf, Startpage with_date,
	// Reddit t, Mojeek since:, Yep start_crawl_date). Values match SearXNG:
	// "day", "week", "month", "year".
	TimeRange string

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

	// BlockCache, when non-nil, is updated on captcha/rate-limit detections:
	// collectResults calls Mark(engine-label) so subsequent fan-outs can skip
	// the doomed direct attempt via IsBlocked. When nil (the default for all
	// existing callers), no Mark is called — byte-identical behaviour preserved.
	// Wired by Phase 2 (P2) of the captcha-aware ox-browser escalation plan.
	BlockCache *fetch.DirectBlockCache

	// OxBrowserFetch, when non-nil, activates the post-fan-out captcha-escalation
	// tier. It fetches the rendered SERP HTML for a given GET URL via the ox-browser
	// stealth Chromium (/fetch endpoint). Injected by go-search (P4); nil (default)
	// preserves the pre-P2 byte-identical path.
	OxBrowserFetch func(ctx context.Context, url string) (string, error)

	// OxEscalate is the code-defined set of engine labels eligible for ox-browser
	// escalation when captcha-blocked. Only GET-fetchable engines with a known SERP
	// URL builder and HTML parser are valid v1 members: {"ddg", "brave"}.
	// Nil or empty → tier inactive.
	OxEscalate []string

	// OxConcurrency caps concurrent ox-browser fetch calls within a single
	// SearchDirect invocation. Default 2 when zero. Admission uses TryAcquire
	// (non-blocking select) to skip escalation rather than queue on the shared
	// resource.
	OxConcurrency int

	// OxRenderDeadline is the per-render-call timeout applied inside runOxEngine
	// before calling OxBrowserFetch. Each engine's render call gets its own
	// independent deadline, so a hung render (e.g. DDG anti-bot stall ~20 s) cannot
	// block a fast one (Brave ~1.5 s). Default defaultOxRenderDeadline (8 s) when zero.
	OxRenderDeadline time.Duration

	// Pacer, when non-nil, applies proactive per-engine human-like spacing before
	// each direct scraper issues its outbound HTTP request. Keyed by engine label
	// so different engines in the parallel fan-out are not serialized — only
	// repeated requests to the SAME engine within a burst (e.g. go-job's
	// multi-query sweep) are spaced.
	//
	// The first request for any key is always immediate (KeyedPacer first-hit
	// semantics), so a single-query fan-out (one hit per engine) incurs zero delay.
	//
	// Use NewScraperPacer to construct a pacer from SCRAPER_PACE_MIN_MS /
	// SCRAPER_PACE_JITTER_MS environment variables. Nil disables pacing entirely.
	Pacer *ratelimit.KeyedPacer
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

// DirectStats summarises the per-leg outcome of a SearchDirect fan-out.
//
// The primary caller signal: Attempted > 0 && OK == 0 means every launched leg
// was blocked or failed — the DC-IP / censorship degraded mode that was previously
// indistinguishable from genuine zero results at the call site.
//
// Accounting:
//   - Attempted = legs that reached the channel (launched goroutines that completed
//     or timed out). IsBlocked-skipped legs are NOT counted here.
//   - OK        = legs that returned ≥1 result.
//   - Empty     = legs that returned 0 results without error (silent-block signature).
//   - Failed    = legs that returned any error (captcha / timeout / blocked / network).
//   - Invariant: Attempted == OK + Empty + Failed.
type DirectStats struct {
	Attempted int
	OK        int
	Empty     int
	Failed    int
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
		// If fn returned context.DeadlineExceeded because it detected the
		// per-source srcCtx deadline (common for HTTP clients that propagate
		// ctx cancellation), reconcile to the srcCtx cause. This collapses
		// the select race: whether done or srcCtx.Done() fires first, both
		// branches produce the same error for handleSourceError to classify.
		if err != nil && srcCtx.Err() != nil && errors.Is(err, context.DeadlineExceeded) {
			err = context.Cause(srcCtx)
		}
	case <-srcCtx.Done():
		// Use context.Cause to distinguish a genuine per-source deadline
		// (returns errPerSourceTimeout, set by WithTimeoutCause in SearchDirect)
		// from an inherited parent cancellation or deadline (returns
		// context.Canceled or context.DeadlineExceeded from the parent).
		// handleSourceError keys on errPerSourceTimeout to Mark allowlisted
		// engines; a parent-propagated DeadlineExceeded must NOT Mark.
		err = context.Cause(srcCtx)
	}
	ch <- directResult{label: label, results: res, err: err, dur: time.Since(start)}
}

// metricSourceResult is the per-source fan-out outcome counter. Encoded as
// name{source=<label>,outcome=ok|empty|captcha|timeout|blocked|fail} so the
// go-kit/metrics Prometheus bridge surfaces it as
// go_search_source_result_total{source="yep",outcome="fail"}.
//
// Outcomes:
//   - ok      — source returned ≥1 result
//   - empty   — source returned HTTP 200 with zero results (silent-block signature:
//     e.g. mojeek 403 masquerading as empty, geo-blocked source)
//   - captcha — source returned *ErrRateLimited (anti-bot block); engine marked in BlockCache
//   - timeout — per-source deadline fired (errPerSourceTimeout via context.Cause) for an
//     OxEscalate-allowlisted engine; engine marked in BlockCache so runOxEscalation
//     promotes it to the stealth-render tier. An inherited parent deadline (which
//     surfaces as context.DeadlineExceeded, not errPerSourceTimeout) does NOT produce
//     this outcome — it falls to fail instead.
//   - blocked — allowlisted engine returned any other hard error (e.g. DDG d.js anti-bot
//     JS challenge body that the JSON parser rejects); engine marked in BlockCache
//   - fail    — source returned any other error (incl. context.Canceled early-return
//     and context.DeadlineExceeded inherited from the parent ctx)
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

// metricOxEscalation is the ox-browser captcha-escalation tier outcome counter (RED signal).
// Encoded as go_search_ox_escalation_total{engine=<label>,outcome=ok|empty|fail|skipped}
const metricOxEscalation = "go_search_ox_escalation_total"

// metricOxInflight is the ox-browser escalation concurrency gauge (USE signal — semaphore depth).
// Encoded as go_search_ox_browser_inflight; carries the go_search_ prefix to match the sibling
// counters and stay grouped in go-search dashboards/alerts (the ox-browser /fetch server in
// go-wowa exposes its own metrics — a bare name would alias against them under PromQL).
const metricOxInflight = "go_search_ox_browser_inflight"

// recordOxEscalation increments the ox-browser escalation outcome counter. Nil-safe.
func recordOxEscalation(m *metrics.Registry, engine, outcome string) {
	if m == nil {
		return
	}
	m.Incr(kitmetrics.Label(metricOxEscalation, "engine", engine, "outcome", outcome))
}

// SearchDirect queries enabled direct scrapers in parallel.
// Returns merged results and per-leg outcome stats. Failures are non-fatal.
// DirectStats.Attempted > 0 && DirectStats.OK == 0 signals that every enabled
// leg was blocked or failed (DC-IP block, censorship degraded mode), which is
// otherwise indistinguishable from a genuine zero-result fan-out.
func SearchDirect(ctx context.Context, cfg DirectConfig, query, language string) ([]sources.Result, DirectStats) {
	if cfg.Browser == nil {
		slog.Info("search direct: browser nil, skipping all scrapers")
		return nil, DirectStats{}
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
			return y.Search(ctx, query, websearch.SearchOpts{TimeRange: cfg.TimeRange})
		}},
		{cfg.Yandex.APIKey != "", "yandex", func(ctx context.Context) ([]sources.Result, error) {
			return SearchYandexAPI(ctx, cfg.Yandex, query, "", cfg.TimeRange, cfg.Metrics)
		}},
		// Wikipedia and Marginalia do not support recency filtering, so skip them
		// when a time_range is requested to avoid polluting the result set with old
		// pages.
		{cfg.Wikipedia && cfg.TimeRange == "", "wikipedia", func(ctx context.Context) ([]sources.Result, error) {
			return runWikipedia(ctx, cfg, query, language)
		}},
		{cfg.Marginalia && cfg.TimeRange == "", "marginalia", func(ctx context.Context) ([]sources.Result, error) {
			return runMarginalia(ctx, cfg, query)
		}},
		{cfg.Mojeek, "mojeek", func(ctx context.Context) ([]sources.Result, error) {
			return runMojeek(ctx, cfg, query)
		}},
	}

	applyPacing(jobs, cfg.Pacer)

	enabled := 0
	for _, j := range jobs {
		if j.enabled {
			enabled++
		}
	}
	if enabled == 0 {
		return nil, DirectStats{}
	}

	ch := make(chan directResult, enabled)
	var wg sync.WaitGroup
	for _, j := range jobs {
		if !j.enabled {
			continue
		}
		// IsBlocked short-circuit: skip the doomed direct attempt for engines already
		// captcha-marked in BlockCache; runOxEscalation (post-fan-out) handles them.
		// Reclaims the per-source timeout budget and naturally rate-limits Chromium
		// re-probes via the BlockCache TTL. When BlockCache is nil (legacy path), no-op.
		if cfg.BlockCache != nil && cfg.BlockCache.IsBlocked(j.label) {
			slog.Debug("search direct: skipping captcha-blocked engine",
				slog.String("source", j.label))
			continue
		}
		wg.Add(1)
		go func(label string, fn func(context.Context) ([]sources.Result, error)) {
			defer wg.Done()
			srcCtx, srcCancel := context.WithTimeoutCause(allDoneCtx, perSrc, errPerSourceTimeout)
			defer srcCancel()
			runSourceWithTimeout(srcCtx, label, fn, ch)
		}(j.label, j.fn)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	merged, stats := collectResults(ch, cfg.Metrics, earlyAt, allDoneCancel, cfg.BlockCache, cfg.OxEscalate)
	// Post-fan-out ox-browser captcha-escalation tier.
	// Dormant unless OxBrowserFetch, OxEscalate, and BlockCache are all set.
	// Ox results are NOT counted in DirectStats — they are a secondary tier, not
	// primary direct legs; stats reflects only the initial fan-out.
	if ox := runOxEscalation(ctx, cfg, query, len(merged), earlyAt); len(ox) > 0 {
		merged = append(merged, ox...)
	}
	return merged, stats
}

// handleSourceError classifies a non-nil source error, records the outcome metric,
// logs a warning, and marks the engine in blockCache when appropriate.
//
// Classification (evaluated in order):
//   - *ErrRateLimited        → outcome="captcha"; always Marks when blockCache != nil.
//   - context.DeadlineExceeded AND label in oxEscalate → outcome="timeout"; Marks.
//     context.Canceled (parent early-return) does NOT match DeadlineExceeded, so
//     user-triggered cancellation is never treated as a transport block.
//   - any other non-nil error AND label in oxEscalate AND NOT context.Canceled
//     → outcome="blocked"; Marks. This catches anti-bot JS challenges (e.g. DDG
//     d.js), HTTP-block responses, and parse errors from a datacenter-detected IP.
//     These are semantically identical to a timeout block: the direct path is
//     unusable and stealth-render is the only working option.
//   - anything else          → outcome="fail"; never Marks.
//
// The allowlist guard (oxEscalate) ensures only configured engines ({ddg, brave})
// are marked and escalated; a random source failing stays outcome="fail" and is
// never promoted to the Chromium render tier. When oxEscalate is nil/empty the
// blocked case never fires — behaviour is byte-identical to v1.44.
func handleSourceError(r directResult, m *metrics.Registry, blockCache *fetch.DirectBlockCache, oxEscalate []string) {
	var rl *ErrRateLimited
	switch {
	case errors.As(r.err, &rl):
		recordSourceResult(m, r.label, "captcha")
		slog.Warn("search source captcha/rate-limited",
			slog.String("source", r.label),
			slog.String("engine", rl.Engine),
		)
		if blockCache != nil {
			blockCache.Mark(r.label)
		}
	case errors.Is(r.err, errPerSourceTimeout) && slices.Contains(oxEscalate, r.label):
		recordSourceResult(m, r.label, "timeout")
		slog.Warn("search source transport-timeout; scheduling ox escalation",
			slog.String("source", r.label),
		)
		if blockCache != nil {
			blockCache.Mark(r.label)
		}
	case !errors.Is(r.err, context.Canceled) && !errors.Is(r.err, context.DeadlineExceeded) && slices.Contains(oxEscalate, r.label):
		recordSourceResult(m, r.label, "blocked")
		slog.Warn("search source blocked (non-timeout hard failure); scheduling ox escalation",
			slog.String("source", r.label),
			slog.Any("error", r.err),
		)
		if blockCache != nil {
			blockCache.Mark(r.label)
		}
	default:
		recordSourceResult(m, r.label, "fail")
		slog.Warn("search source failed", slog.String("source", r.label), slog.Any("error", r.err))
	}
}

// collectResults drains ch and accumulates results, emitting metrics and triggering
// early-return cancellation once earlyAt results are collected.
//
// captcha outcome: when a source's error matches *ErrRateLimited (anti-bot signal
// already emitted by the ddg/brave/bing parsers), the outcome is "captcha" rather
// than "fail", and blockCache.Mark is called if blockCache is non-nil. This keeps
// legit-empty (nil error, zero results → "empty") semantically distinct from
// blocked-engine ("captcha") — result-count is never used as the captcha signal.
//
// timeout outcome: when a source's error is context.DeadlineExceeded (per-source
// deadline fired — NOT context.Canceled which is a parent early-return) AND the
// source label is present in oxEscalate, the outcome is "timeout" and
// blockCache.Mark is called so runOxEscalation will escalate to stealth-render.
// A timeout from a NON-allowlisted source stays outcome="fail" and is NOT marked
// — over-escalation guard: a random slow source must not trigger an expensive
// Chromium render. When oxEscalate is nil/empty no timeout is ever marked
// (dormant-byte-identical with the pre-P2 path).
//
// blocked outcome: any other non-nil, non-Canceled error from an allowlisted engine
// (e.g. a DDG anti-bot JS challenge returning a non-JSON body, or an HTTP-block
// response) is classified as outcome="blocked" and Marks the engine — identical
// escalation semantics to timeout. This closes the detection gap where d.js
// challenges were silently swallowed as outcome="fail" without ever triggering the
// stealth-render tier.
func collectResults(ch <-chan directResult, m *metrics.Registry, earlyAt int, cancel context.CancelFunc, blockCache *fetch.DirectBlockCache, oxEscalate []string) ([]sources.Result, DirectStats) {
	var all []sources.Result
	var stats DirectStats
	var cancelled bool
	for r := range ch {
		stats.Attempted++
		if m != nil {
			m.ObserveSeconds(
				kitmetrics.Label("go_search_search_source_duration_seconds", "source", r.label),
				r.dur,
			)
		}
		if r.err != nil {
			stats.Failed++
			handleSourceError(r, m, blockCache, oxEscalate)
			continue
		}
		if len(r.results) == 0 {
			stats.Empty++
			recordSourceResult(m, r.label, "empty")
			slog.Info("search source empty", slog.String("source", r.label))
			continue
		}
		stats.OK++
		recordSourceResult(m, r.label, "ok")
		slog.Info("search source results", slog.String("source", r.label), slog.Int("count", len(r.results)))
		all = append(all, r.results...)
		if !cancelled && len(all) >= earlyAt {
			cancel()
			cancelled = true
		}
	}
	return all, stats
}
