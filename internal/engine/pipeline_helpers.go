package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// fetchContentsWithRewriter fetches URL contents, optionally rewriting URLs before fetch.
// Rewritten URLs pointing to raw.githubusercontent.com are fetched as plain text;
// all other URLs use go-readability extraction.
func fetchContentsWithRewriter(ctx context.Context, results []SearxngResult, rewriter func(string) string) map[string]string {
	if rewriter == nil {
		return FetchContentsParallel(ctx, results, nil)
	}

	contents := make(map[string]string)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, r := range results {
		wg.Add(1)
		go func(originalURL string) {
			defer wg.Done()
			fetchURL := rewriter(originalURL)
			var text string
			var err error
			if strings.Contains(fetchURL, "raw.githubusercontent.com") {
				text, err = FetchRawContent(ctx, fetchURL)
			} else {
				_, text, err = FetchURLContent(ctx, fetchURL)
			}
			if err == nil && text != "" {
				mu.Lock()
				contents[originalURL] = text // key is original URL for citation matching
				mu.Unlock()
			}
		}(r.URL)
	}
	wg.Wait()
	return contents
}

// buildRawOutput constructs output for raw mode — clean content without LLM.
func buildRawOutput(query string, results []SearxngResult, contents map[string]string, limit int) SmartSearchOutput {
	var parts []string
	var sources []SourceItem

	for i, r := range results {
		content := contents[r.URL]
		if content == "" {
			content = r.Content // fallback to snippet
		}
		if len(content) > limit {
			content = content[:limit] + "..."
		}

		sources = append(sources, SourceItem{
			Index:   i + 1,
			Title:   r.Title,
			URL:     r.URL,
			Snippet: content,
		})
		parts = append(parts, fmt.Sprintf("### [%d] %s\nSource: %s\n\n%s", i+1, r.Title, r.URL, content))
	}

	answer := fmt.Sprintf("Found %d results for: %s\n\n", len(results), query)
	for _, p := range parts {
		answer += p + "\n\n---\n\n" //nolint:perfsprint // simple readable concatenation
	}

	return SmartSearchOutput{
		Query:   query,
		Answer:  answer,
		Sources: sources,
	}
}
