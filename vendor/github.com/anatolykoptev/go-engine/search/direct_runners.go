package search

import (
	"context"
	"log/slog"
	"strings"

	"github.com/anatolykoptev/go-engine/fetch"
	"github.com/anatolykoptev/go-engine/sources"
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

// runReddit waits on the optional rate limiter then fetches Reddit results.
func runReddit(ctx context.Context, cfg DirectConfig, query string) ([]sources.Result, error) {
	if cfg.RedditLimiter != nil {
		if err := cfg.RedditLimiter.Wait(ctx); err != nil {
			slog.Debug("reddit rate limit wait", slog.Any("error", err))
			return nil, nil //nolint:nilerr // limiter cancelled: skip engine
		}
	}
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
func runMojeek(ctx context.Context, cfg DirectConfig, query string) ([]sources.Result, error) {
	return SearchMojeekDirect(ctx, cfg.Browser, query, cfg.Metrics)
}
