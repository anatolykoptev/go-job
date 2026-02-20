package engine

import (
	"context"
	"sync"
)

// DefaultOutputOpts is the compact default for pipeline-based tools.
var DefaultOutputOpts = OutputOpts{
	MaxAnswerChars:  3000,
	MaxSources:      8,
	IncludeSnippets: false,
}

// FormatOutput trims SmartSearchOutput to fit within the given budget.
func FormatOutput(out SmartSearchOutput, opts OutputOpts) SmartSearchOutput {
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

// fetchContentsParallel fetches text content from URLs in parallel.
func fetchContentsParallel(ctx context.Context, results []SearxngResult) map[string]string {
	contents := make(map[string]string)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, r := range results {
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			_, text, err := FetchURLContent(ctx, u)
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

// BuildSearchOutput constructs SmartSearchOutput with sources and facts.
func BuildSearchOutput(query string, llmOut *LLMStructuredOutput, results []SearxngResult) SmartSearchOutput {
	output := SmartSearchOutput{
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
