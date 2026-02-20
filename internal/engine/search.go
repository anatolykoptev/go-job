package engine

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
)

// SearchSearXNG queries the SearXNG instance and returns raw results.
func SearchSearXNG(ctx context.Context, query, language, timeRange, engines string) ([]SearxngResult, error) {
	u, err := url.Parse(cfg.SearxngURL + "/search")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("format", "json")
	if language != "" && language != "all" {
		q.Set("language", language)
	}
	if timeRange != "" {
		q.Set("time_range", timeRange)
	}
	if engines != "" {
		q.Set("engines", engines)
	}
	u.RawQuery = q.Encode()

	metrics.SearchRequests.Add(1)

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := RetryHTTP(ctx, DefaultRetryConfig, func() (*http.Response, error) {
		return cfg.HTTPClient.Do(req)
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var data searxngResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return data.Results, nil
}

// FilterByScore removes results below minScore, keeping at least minKeep.
func FilterByScore(results []SearxngResult, minScore float64, minKeep int) []SearxngResult {
	var out []SearxngResult
	for _, r := range results {
		if r.Score >= minScore {
			out = append(out, r)
		}
	}
	if len(out) < minKeep && len(results) >= minKeep {
		return results[:minKeep]
	}
	if len(out) < minKeep {
		return results
	}
	return out
}

// SearchDirect queries enabled direct scrapers in parallel.
// Returns merged results from all direct sources. Failures are non-fatal.
func SearchDirect(ctx context.Context, query, language string) []SearxngResult {
	if cfg.BrowserClient == nil {
		return nil
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var all []SearxngResult

	if cfg.DirectDDG {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, err := RetryDo(ctx, DefaultRetryConfig, func() ([]SearxngResult, error) {
				return SearchDDGDirect(ctx, cfg.BrowserClient, query, "wt-wt")
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

	if cfg.DirectStartpage {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, err := RetryDo(ctx, DefaultRetryConfig, func() ([]SearxngResult, error) {
				return SearchStartpageDirect(ctx, cfg.BrowserClient, query, language)
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

// DedupByDomain limits results to maxPerDomain per domain.
func DedupByDomain(results []SearxngResult, maxPerDomain int) []SearxngResult {
	counts := make(map[string]int)
	var out []SearxngResult
	for _, r := range results {
		u, err := url.Parse(r.URL)
		if err != nil {
			continue
		}
		domain := u.Hostname()
		if counts[domain] < maxPerDomain {
			out = append(out, r)
			counts[domain]++
		}
	}
	return out
}
