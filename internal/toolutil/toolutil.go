// Package toolutil provides shared helper functions for go_job MCP tools.
// Currently delegates to go-search engine for cache and fetch operations.
// TODO: migrate engine dependencies to go_job/internal/engine/ to decouple from go-search.
package toolutil

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// NormLang normalises a language field: empty string → "all".
func NormLang(lang string) string {
	if lang == "" {
		return "all"
	}
	return lang
}

// CacheLoadJSON tries to load a cached value of type T from the engine cache.
// Returns the decoded value and true on hit; zero value and false on miss or decode error.
func CacheLoadJSON[T any](ctx context.Context, key string) (T, bool) {
	cached, ok := engine.CacheGet(ctx, key)
	if !ok {
		var zero T
		return zero, false
	}
	var out T
	if err := json.Unmarshal([]byte(cached.Answer), &out); err != nil {
		var zero T
		return zero, false
	}
	return out, true
}

// CacheStoreJSON marshals v and stores it in the engine cache.
func CacheStoreJSON[T any](ctx context.Context, key, query string, v T) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	engine.CacheSet(ctx, key, engine.SmartSearchOutput{
		Query:  query,
		Answer: string(data),
	})
}

// FetchURLsParallel fetches URL content for each result whose URL is NOT in skipURLs.
// Returns a map of url → fetched text.
func FetchURLsParallel(ctx context.Context, results []engine.SearxngResult, skipURLs map[string]bool) map[string]string {
	contents := make(map[string]string, len(results))
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, r := range results {
		if skipURLs[r.URL] {
			continue
		}
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			_, text, err := engine.FetchURLContent(ctx, u)
			if err == nil && text != "" {
				mu.Lock()
				contents[u] = text
				mu.Unlock()
			}
		}(r.URL)
	}
	wg.Wait()
	return contents
}
