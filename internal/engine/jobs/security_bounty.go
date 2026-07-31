package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/anatolykoptev/go_job/internal/engine"
)

const securityCacheKey = "security_programs"

// securityBodyLimit is the DoS ceiling for security-source response bodies
// (hackerone/bugcrowd/intigriti/yeswehack/federacy via security_bounty.go, and
// the immunefi/sherlock/gowowa_render readers that share this cap).
//
// hackerone_data.json measured 17,777,018 bytes (17.8 MB) on 2026-07-30 and
// grows over time; the old 10 MB cap silently truncated it (io.LimitReader
// stops without error), producing a misleading "parse failed" log. 64 MiB
// gives ~3.6x headroom over the measured size and matches the ATS board
// ceiling (atsBoardMaxBytes). A var (not const) so truncation tests can
// override it with a small cap, mirroring atsBoardMaxBytes.
var securityBodyLimit int64 = 64 * 1024 * 1024 // 64 MiB

var securitySources = []struct {
	url      string
	platform string
	parser   func([]byte) ([]engine.SecurityProgram, error)
}{
	{
		url:      "https://raw.githubusercontent.com/arkadiyt/bounty-targets-data/main/data/hackerone_data.json",
		platform: sourceHackerOne,
		parser:   parseHackerOneData,
	},
	{
		url:      "https://raw.githubusercontent.com/arkadiyt/bounty-targets-data/main/data/bugcrowd_data.json",
		platform: "bugcrowd",
		parser:   parseBugcrowdData,
	},
	{
		url:      "https://raw.githubusercontent.com/arkadiyt/bounty-targets-data/main/data/intigriti_data.json",
		platform: "intigriti",
		parser:   parseIntigritiData,
	},
	{
		url:      "https://raw.githubusercontent.com/arkadiyt/bounty-targets-data/main/data/yeswehack_data.json",
		platform: "yeswehack",
		parser:   parseYesWeHackData,
	},
	{
		url:      "https://raw.githubusercontent.com/arkadiyt/bounty-targets-data/main/data/federacy_data.json",
		platform: "federacy",
		parser:   parseFederacyData,
	},
}

// SearchSecurityPrograms fetches bug bounty programs from multiple platforms.
// Results are cached.
func SearchSecurityPrograms(ctx context.Context, limit int) ([]engine.SecurityProgram, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}

	if cached, ok := engine.CacheLoadJSON[[]engine.SecurityProgram](ctx, securityCacheKey); ok {
		slog.Debug("security: using cached results", slog.Int("results", len(cached)))
		if len(cached) > limit {
			cached = cached[:limit]
		}
		return cached, nil
	}

	programs, err := fetchAllSecurityPrograms(ctx)
	if err != nil {
		return nil, err
	}

	engine.CacheStoreJSON(ctx, securityCacheKey, "", programs)
	if len(programs) > limit {
		programs = programs[:limit]
	}

	slog.Debug("security: fetch complete", slog.Int("results", len(programs)))
	return programs, nil
}

func fetchAllSecurityPrograms(ctx context.Context) ([]engine.SecurityProgram, error) {
	var all []engine.SecurityProgram
	var lastErr error

	for _, src := range securitySources {
		data, err := fetchSecuritySource(ctx, src.url)
		if err != nil {
			slog.Warn("security: source fetch failed",
				slog.String("platform", src.platform),
				slog.Any("error", err))
			lastErr = err
			continue
		}

		programs, err := src.parser(data)
		if err != nil {
			slog.Warn("security: parse failed",
				slog.String("platform", src.platform),
				slog.Any("error", err))
			lastErr = err
			continue
		}

		all = append(all, programs...)
	}

	if len(all) == 0 && lastErr != nil {
		return nil, fmt.Errorf("security: all sources failed: %w", lastErr)
	}

	return all, nil
}

func fetchSecuritySource(ctx context.Context, url string) ([]byte, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", engine.UserAgentBot)

	resp, err := engine.Cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("security request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("security source returned status %d", resp.StatusCode)
	}

	body, err := readLimitedBody(resp.Body, securityBodyLimit)
	if err != nil {
		return nil, fmt.Errorf("security: read body: %w", err)
	}

	return body, nil
}
