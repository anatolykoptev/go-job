package search

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/anatolykoptev/go-engine/fetch"
	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-engine/websearch"
)

// runDDG waits on the optional rate limiter then fetches DDG results.
func runDDG(ctx context.Context, cfg DirectConfig, query string) ([]sources.Result, error) {
	if cfg.DDGLimiter != nil {
		if err := cfg.DDGLimiter.Wait(ctx); err != nil {
			slog.Debug("ddg rate limit wait", slog.Any("error", err))
			return nil, nil //nolint:nilerr // limiter cancelled: skip engine
		}
	}
	return fetch.RetryDo(ctx, cfg.Retry, func() ([]sources.Result, error) {
		return SearchDDGDirect(ctx, cfg.Browser, query, "wt-wt", cfg.Metrics)
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
		return SearchStartpageDirect(ctx, cfg.Browser, query, language, cfg.Metrics)
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
		return SearchBraveDirect(ctx, cfg.Browser, query, cfg.Metrics)
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
		return SearchRedditDirect(ctx, cfg.Browser, query, cfg.Metrics)
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
		return SearchBingDirect(ctx, cfg.Browser, query, cfg.Metrics)
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
	return SearchMojeekDirect(ctx, browser, query, cfg.Metrics)
}
