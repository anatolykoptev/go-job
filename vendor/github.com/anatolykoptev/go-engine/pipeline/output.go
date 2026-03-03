// Package pipeline provides output formatting and parallel fetch utilities
// for building search-to-LLM pipelines.
package pipeline

import (
	"context"
	"sync"

	"github.com/anatolykoptev/go-engine/llm"
	"github.com/anatolykoptev/go-engine/search"
)

// SourceItem represents a single search result source in the output.
type SourceItem struct {
	Index   int    `json:"index"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

// SearchOutput is the complete structured output of a search pipeline.
type SearchOutput struct {
	Query   string         `json:"query"`
	Answer  string         `json:"answer"`
	Facts   []llm.FactItem `json:"facts"`
	Sources []SourceItem   `json:"sources"`
}

// OutputOpts controls the size and shape of SearchOutput.
type OutputOpts struct {
	MaxAnswerChars  int  // truncate LLM answer (0 = no limit)
	MaxSources      int  // max sources in output (0 = all)
	IncludeSnippets bool // include snippet text in sources
}

// DefaultOutputOpts is a compact default for pipeline-based tools.
var DefaultOutputOpts = OutputOpts{
	MaxAnswerChars:  3000,
	MaxSources:      8,
	IncludeSnippets: false,
}

// FormatOutput trims SearchOutput to fit within the given budget.
func FormatOutput(out SearchOutput, opts OutputOpts) SearchOutput {
	if opts.MaxAnswerChars > 0 && len(out.Answer) > opts.MaxAnswerChars {
		out.Answer = out.Answer[:opts.MaxAnswerChars] + "..."
	}
	if !opts.IncludeSnippets {
		for i := range out.Sources {
			out.Sources[i].Snippet = ""
		}
	}
	if opts.MaxSources > 0 && len(out.Sources) > opts.MaxSources {
		out.Sources = out.Sources[:opts.MaxSources]
	}
	return out
}

// BuildSearchOutput constructs SearchOutput from LLM results and search results.
func BuildSearchOutput(query string, llmOut *llm.StructuredOutput, results []search.Result) SearchOutput {
	output := SearchOutput{
		Query:  query,
		Answer: llmOut.Answer,
		Facts:  llmOut.Facts,
	}
	for i, r := range results {
		output.Sources = append(output.Sources, SourceItem{
			Index:   i + 1,
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Content,
		})
	}
	return output
}

// ParallelFetch fetches URL content in parallel, returning url->text map.
// fetchFn determines how each URL is fetched; errors are silently skipped.
func ParallelFetch(ctx context.Context, urls []string, fetchFn func(ctx context.Context, url string) (string, error)) map[string]string {
	contents := make(map[string]string, len(urls))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, u := range urls {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			t, err := fetchFn(ctx, url)
			if err == nil && t != "" {
				mu.Lock()
				contents[url] = t
				mu.Unlock()
			}
		}(u)
	}
	wg.Wait()
	return contents
}
