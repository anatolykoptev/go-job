//nolint:goconst
package search

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/anatolykoptev/go-engine/fetch"
	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-engine/websearch"
)

// defaultOxRenderDeadline is the per-render-call timeout applied in runOxEngine
// when DirectConfig.OxRenderDeadline is zero. 8 s bounds a stuck stealth render
// (e.g. DDG anti-bot navigation stall ~20 s) so a hung engine never blocks the
// synchronous search response for the full go-wowa navigation deadline.
const defaultOxRenderDeadline = 8 * time.Second

// runDDG waits on the optional rate limiter then fetches DDG results.
func runDDG(ctx context.Context, cfg DirectConfig, query string) ([]sources.Result, error) {
	if cfg.DDGLimiter != nil {
		if err := cfg.DDGLimiter.Wait(ctx); err != nil {
			slog.Debug("ddg rate limit wait", slog.Any("error", err))
			return nil, nil //nolint:nilerr // limiter cancelled: skip engine
		}
	}
	return fetch.RetryDo(ctx, cfg.Retry, func() ([]sources.Result, error) {
		return SearchDDGDirect(ctx, cfg.Browser, query, "wt-wt", cfg.Metrics, cfg.TimeRange)
	})
}

// runStartpage waits on the optional rate limiter then fetches Startpage results.
func runStartpage(ctx context.Context, cfg DirectConfig, query, language string) ([]sources.Result, error) {
	if cfg.StartpageLimiter != nil {
		if err := cfg.StartpageLimiter.Wait(ctx); err != nil {
			slog.Debug("startpage rate limit wait", slog.Any("error", err))
			return nil, nil //nolint:nilerr // limiter cancelled: skip engine
		}
	}
	return fetch.RetryDo(ctx, cfg.Retry, func() ([]sources.Result, error) {
		return SearchStartpageDirect(ctx, cfg.Browser, query, language, cfg.Metrics, cfg.TimeRange)
	})
}

// runBrave waits on the optional rate limiter then fetches Brave results.
func runBrave(ctx context.Context, cfg DirectConfig, query string) ([]sources.Result, error) {
	if cfg.BraveLimiter != nil {
		if err := cfg.BraveLimiter.Wait(ctx); err != nil {
			slog.Debug("brave rate limit wait", slog.Any("error", err))
			return nil, nil //nolint:nilerr // limiter cancelled: skip engine
		}
	}
	return fetch.RetryDo(ctx, cfg.Retry, func() ([]sources.Result, error) {
		return SearchBraveDirect(ctx, cfg.Browser, query, cfg.Metrics, cfg.TimeRange)
	})
}

// searchRedditOAuth calls websearch.SearchOAuth with the token manager and
// user-agent from cfg.
func searchRedditOAuth(ctx context.Context, cfg DirectConfig, query string) ([]sources.Result, error) {
	return websearch.SearchOAuth(ctx, cfg.Browser, cfg.RedditTokenManager, query, cfg.RedditUserAgent)
}

// isEscalatable reports whether err should cause the Reddit tier orchestrator
// to try the next tier. Only rate-limit / exhaustion errors escalate; parse
// errors, context cancellation, and ErrCredentialInvalid do not.
//
// ErrCredentialInvalid is deliberately non-escalatable here: the orchestrator
// handles it via a dedicated credInvalid flag (skip Tier 2, try Tier 3).
func isEscalatable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, websearch.ErrCredentialInvalid) {
		return false
	}
	if errors.Is(err, websearch.ErrTransient) {
		return true
	}
	var rl *ErrRateLimited
	return errors.As(err, &rl)
}

// tierCallResult is the shared outcome type for a Reddit tier call.
// done == true means the caller should return (res, err) immediately.
// skipNext signals that the next tier should be bypassed.
type tierCallResult struct {
	res      []sources.Result
	err      error
	done     bool // return immediately (success or non-escalatable error)
	skipNext bool // skip the immediately following tier (ErrCredentialInvalid)
}

// callTier1OAuth executes Tier 1 (OAuth) and classifies the outcome for runReddit.
func callTier1OAuth(ctx context.Context, cfg DirectConfig, query string) tierCallResult {
	res, err := searchRedditOAuth(ctx, cfg, query)
	if err == nil {
		if len(res) > 0 {
			return tierCallResult{res: res, done: true}
		}
		// empty primary result escalates — a soft-block/degraded primary should let lower tiers try
		recordTierOutcome("oauth", nil)
		return tierCallResult{}
	}
	if errors.Is(err, websearch.ErrCredentialInvalid) {
		recordTierOutcome("oauth", err)
		return tierCallResult{skipNext: true}
	}
	if !isEscalatable(err) {
		return tierCallResult{res: res, err: err, done: true}
	}
	recordTierOutcome("oauth", err)
	return tierCallResult{}
}

// callTierFn executes a generic cookie/render tier function and classifies the outcome.
func callTierFn(ctx context.Context, tierName, query string, fn func(context.Context, string) ([]sources.Result, error)) tierCallResult {
	res, err := fn(ctx, query)
	if err == nil {
		if len(res) > 0 {
			return tierCallResult{res: res, done: true}
		}
		recordTierOutcome(tierName, nil)
		return tierCallResult{}
	}
	if !isEscalatable(err) {
		return tierCallResult{res: res, err: err, done: true}
	}
	recordTierOutcome(tierName, err)
	return tierCallResult{}
}

// callTierFnFinal executes a terminal tier function (Tier 3) and classifies the outcome.
// Unlike callTierFn, non-escalatable and escalatable errors are treated the same
// (both record + return graceful-empty) because there is no further tier to escalate to.
func callTierFnFinal(ctx context.Context, query string, fn func(context.Context, string) ([]sources.Result, error)) tierCallResult {
	res, err := fn(ctx, query)
	if err == nil && len(res) > 0 {
		return tierCallResult{res: res, done: true}
	}
	recordTierOutcome("render", err)
	return tierCallResult{}
}

// runReddit implements the 3-tier Reddit escalation chain.
//
// Tier activation (each independently optional, all nil → legacy):
//
//	Tier 1 — OAuth         (cfg.RedditTokenManager != nil)
//	Tier 2 — Cookie search (cfg.RedditCookieSearch != nil)
//	Tier 3 — Browser render(cfg.RedditBrowserRender != nil)
//
// Legacy invariant (CRITICAL): when all three tier fields are nil, the function
// falls through to SearchRedditDirect — byte-for-byte identical behaviour to
// the pre-Phase-2 implementation. Callers that have not set any tier fields see
// zero behaviour change.
//
// ErrCredentialInvalid from Tier 1 skips Tier 2 (same account) and falls to Tier 3.
// All active tiers fell through → nil, nil (graceful-empty).
func runReddit(ctx context.Context, cfg DirectConfig, query string) ([]sources.Result, error) {
	if cfg.RedditLimiter != nil {
		if err := cfg.RedditLimiter.Wait(ctx); err != nil {
			slog.Debug("reddit rate limit wait", slog.Any("error", err))
			return nil, nil //nolint:nilerr // limiter cancelled: skip engine
		}
	}

	anyTierActive := cfg.RedditTokenManager != nil ||
		cfg.RedditCookieSearch != nil ||
		cfg.RedditBrowserRender != nil

	skipTier2 := false

	if cfg.RedditTokenManager != nil {
		t1 := callTier1OAuth(ctx, cfg, query)
		if t1.done {
			return t1.res, t1.err
		}
		skipTier2 = t1.skipNext
	}

	if cfg.RedditCookieSearch != nil && !skipTier2 {
		t2 := callTierFn(ctx, "cookie", query, cfg.RedditCookieSearch)
		if t2.done {
			return t2.res, t2.err
		}
	}

	if cfg.RedditBrowserRender != nil {
		t3 := callTierFnFinal(ctx, query, cfg.RedditBrowserRender)
		if t3.done {
			return t3.res, t3.err
		}
	}

	if anyTierActive {
		return nil, nil
	}

	// LEGACY PATH (all tier fields nil): unchanged behaviour.
	return fetch.RetryDo(ctx, cfg.Retry, func() ([]sources.Result, error) {
		return SearchRedditDirect(ctx, cfg.Browser, query, cfg.Metrics, cfg.TimeRange)
	})
}

// runBing waits on the optional rate limiter then fetches Bing results.
func runBing(ctx context.Context, cfg DirectConfig, query string) ([]sources.Result, error) {
	if cfg.BingLimiter != nil {
		if err := cfg.BingLimiter.Wait(ctx); err != nil {
			slog.Debug("bing rate limit wait", slog.Any("error", err))
			return nil, nil //nolint:nilerr // limiter cancelled: skip engine
		}
	}
	return fetch.RetryDo(ctx, cfg.Retry, func() ([]sources.Result, error) {
		return SearchBingDirect(ctx, cfg.Browser, query, cfg.Metrics, cfg.TimeRange)
	})
}

// runWikipedia fetches Wikipedia MediaWiki search results.
// lang is "ru" for Russian-prefixed language, "en" otherwise.
func runWikipedia(ctx context.Context, cfg DirectConfig, query, language string) ([]sources.Result, error) {
	lang := "en"
	if strings.HasPrefix(language, "ru") {
		lang = "ru"
	}
	return SearchWikipediaDirect(ctx, cfg.Browser, query, lang, cfg.Metrics)
}

// runMarginalia fetches Marginalia indie-web search results.
func runMarginalia(ctx context.Context, cfg DirectConfig, query string) ([]sources.Result, error) {
	return SearchMarginaliaDirect(ctx, cfg.Browser, query, cfg.Metrics)
}

// runMojeek fetches Mojeek search results via HTML scraping.
//
// Mojeek is routed through cfg.MojeekBrowser when set (a residential-proxy
// BrowserClient) because its anti-bot gate blocks datacenter egress IPs at the
// network level. When MojeekBrowser is nil it falls back to the shared
// cfg.Browser (direct-primary dualBrowser), preserving prior behaviour.
func runMojeek(ctx context.Context, cfg DirectConfig, query string) ([]sources.Result, error) {
	browser := cfg.Browser
	// isNilInterface (not a plain != nil) guards the typed-nil pitfall: an
	// interface holding a typed-nil *stealth.BrowserClient passes `!= nil` and
	// then panics in Do (the 2026-05-16 go-search prod panic class). Mojeek is
	// the one source that bypasses the dualBrowser funnel where isNilInterface
	// already applies, so the guard must live here too.
	if !isNilInterface(cfg.MojeekBrowser) {
		browser = cfg.MojeekBrowser
	}
	return SearchMojeekDirect(ctx, browser, query, cfg.Metrics, cfg.TimeRange)
}

// runOxEscalation is the post-fan-out captcha-escalation tier that routes
// captcha-blocked search engines through the ox-browser stealth Chromium (/fetch).
//
// Dormant-by-default invariant (CRITICAL): when OxBrowserFetch, OxEscalate, or
// BlockCache are nil/empty, this function returns nil with zero ox-browser calls —
// byte-identical to the pre-P2 SearchDirect path.
//
// Post-fan-out placement: ox-browser /fetch is ~30s; runSourceWithTimeout cancels
// sources at 6s, so escalation MUST run after collectResults, on the original ctx.
//
// EarlyReturnAt short-circuit: if the fan-out already produced ≥earlyAt results,
// skip escalation (existing results suffice — no need to burn Chromium).
//
// Concurrency: a channel-based TryAcquire semaphore (cap=OxConcurrency, default 2)
// bounds concurrent Chromium calls within one SearchDirect invocation. Excess
// requests are skipped (not queued) to avoid stacking on the shared 4-core resource.
//
// Allowlist v1 = {ddg, brave} — GET-fetchable. Startpage is POST-only → excluded
// (ADR-6: widening /fetch to method+body is a guarded one-way SSRF door).
func runOxEscalation(ctx context.Context, cfg DirectConfig, query string, mergedLen, earlyAt int) []sources.Result {
	if cfg.OxBrowserFetch == nil || len(cfg.OxEscalate) == 0 || cfg.BlockCache == nil {
		return nil
	}
	if mergedLen >= earlyAt {
		slog.Debug("ox escalation: fan-out threshold met, skipping",
			slog.Int("merged", mergedLen), slog.Int("earlyAt", earlyAt))
		return nil
	}

	var eligible []string
	for _, label := range cfg.OxEscalate {
		if cfg.BlockCache.IsBlocked(label) {
			eligible = append(eligible, label)
		}
	}
	if len(eligible) == 0 {
		return nil
	}

	slog.Info("ox escalation: starting", slog.Int("engines", len(eligible)))

	concurrency := cfg.OxConcurrency
	if concurrency <= 0 {
		concurrency = 2
	}
	// Channel-based TryAcquire semaphore: send = acquire, recv = release.
	sem := make(chan struct{}, concurrency)

	type oxOut struct{ results []sources.Result }
	resultCh := make(chan oxOut, len(eligible))
	var wg sync.WaitGroup

	for _, label := range eligible {
		// TryAcquire: non-blocking — skip if semaphore full to avoid queuing
		// on the shared Chromium resource (go-wowa ContextPool is the authoritative
		// server-side bound; client TryAcquire is a courtesy first-line cap).
		select {
		case sem <- struct{}{}:
		default:
			slog.Debug("ox escalation: semaphore full, skipping engine", slog.String("engine", label))
			recordOxEscalation(cfg.Metrics, label, "skipped")
			continue
		}
		wg.Add(1)
		go func(l string) {
			defer wg.Done()
			defer func() { <-sem }()
			if cfg.Metrics != nil {
				cfg.Metrics.Gauge(metricOxInflight).Inc()
				defer cfg.Metrics.Gauge(metricOxInflight).Dec()
			}
			res, outcome := runOxEngine(ctx, cfg, query, l)
			recordOxEscalation(cfg.Metrics, l, outcome)
			if len(res) > 0 {
				resultCh <- oxOut{res}
			} else {
				// Render did not revive the engine (outcome=empty or fail).
				// Unmark so the next direct fan-out re-probes instead of
				// staying pinned for the full 10 m TTL. If render had
				// returned results, the Mark stays — engine is genuinely
				// blocked and render is the working path.
				cfg.BlockCache.Unmark(l)
			}
		}(label)
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	var all []sources.Result
	for r := range resultCh {
		all = append(all, r.results...)
	}
	return all
}

// runOxEngine dispatches to the engine-specific ox-browser SERP runner.
// Returns (results, outcome) where outcome is "ok", "empty", or "fail".
//
// Per-render deadline: a context.WithTimeout is applied before each OxBrowserFetch
// call so a hung render (e.g. DDG anti-bot stall ~20 s) fails fast and returns
// outcome="fail" instead of blocking the synchronous SearchDirect response.
// Each engine's call is independently bounded, so one slow engine cannot eat the
// deadline of a fast one. On deadline expiry OxBrowserFetch returns an error →
// outcome="fail" → runOxEscalation records fail + Unmarks the engine (self-heal).
func runOxEngine(ctx context.Context, cfg DirectConfig, query, label string) ([]sources.Result, string) {
	deadline := cfg.OxRenderDeadline
	if deadline <= 0 {
		deadline = defaultOxRenderDeadline
	}
	rctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	switch label {
	case "ddg":
		return runOxDDG(rctx, cfg, query)
	case "brave":
		return runOxBrave(rctx, cfg, query)
	case "bing":
		return runOxBing(rctx, cfg, query)
	default:
		slog.Warn("ox escalation: unsupported engine", slog.String("engine", label))
		return nil, "fail"
	}
}

// runOxDDG fetches and parses a DuckDuckGo HTML SERP via ox-browser stealth Chromium.
// URL built by websearch.DDGHTMLURL (single-owned in websearch per ADR-8).
// Parsed by websearch.ParseDDGHTML (proven in prod via searchViaOxBrowser).
func runOxDDG(ctx context.Context, cfg DirectConfig, query string) ([]sources.Result, string) {
	u := websearch.DDGHTMLURL(query, websearch.SearchOpts{TimeRange: cfg.TimeRange})
	html, err := cfg.OxBrowserFetch(ctx, u)
	if err != nil {
		slog.Warn("ox escalation ddg: fetch error", slog.Any("error", err))
		return nil, "fail"
	}
	results, err := websearch.ParseDDGHTML([]byte(html))
	if err != nil {
		slog.Warn("ox escalation ddg: parse error", slog.Any("error", err))
		return nil, "fail"
	}
	if len(results) == 0 {
		return nil, "empty"
	}
	return results, "ok"
}

// runOxBrave fetches and parses a Brave Search HTML SERP via ox-browser stealth Chromium.
// URL built by websearch.BraveSearchURL; parsed by websearch.ParseBraveHTML.
// Brave is GET-fetchable (ADR-6); Startpage excluded (POST-only → SSRF risk).
func runOxBrave(ctx context.Context, cfg DirectConfig, query string) ([]sources.Result, string) {
	u := websearch.BraveSearchURL(query, websearch.SearchOpts{TimeRange: cfg.TimeRange})
	html, err := cfg.OxBrowserFetch(ctx, u)
	if err != nil {
		slog.Warn("ox escalation brave: fetch error", slog.Any("error", err))
		return nil, "fail"
	}
	results, err := websearch.ParseBraveHTML([]byte(html))
	if err != nil {
		slog.Warn("ox escalation brave: parse error", slog.Any("error", err))
		return nil, "fail"
	}
	if len(results) == 0 {
		return nil, "empty"
	}
	return results, "ok"
}

// runOxBing fetches and parses a Bing HTML SERP via ox-browser stealth Chromium.
// URL built by websearch.BingSearchURL (single-owned in websearch per ADR-8).
// Parsed by websearch.ParseBingHTML (reused from the Bing scraper path).
// Bing is GET-fetchable (ADR-6); Startpage excluded (POST-only → SSRF risk).
func runOxBing(ctx context.Context, cfg DirectConfig, query string) ([]sources.Result, string) {
	u := websearch.BingSearchURL(query, websearch.SearchOpts{TimeRange: cfg.TimeRange})
	html, err := cfg.OxBrowserFetch(ctx, u)
	if err != nil {
		slog.Warn("ox escalation bing: fetch error", slog.Any("error", err))
		return nil, "fail"
	}
	results, err := websearch.ParseBingHTML([]byte(html))
	if err != nil {
		slog.Warn("ox escalation bing: parse error", slog.Any("error", err))
		return nil, "fail"
	}
	if len(results) == 0 {
		return nil, "empty"
	}
	return results, "ok"
}
