// Package pipeline provides output formatting and parallel fetch utilities
// for building search-to-LLM pipelines.
package pipeline

import (
	"github.com/anatolykoptev/go-engine/llm"
	"github.com/anatolykoptev/go-engine/sources"
)

// SourceItem represents a single search result source in the output.
type SourceItem struct {
	Index   int    `json:"index"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

// SearchOutput is the complete structured output of a search pipeline.
//
// LLMSkipped/DegradeReason mark output produced by a no-LLM degrade path
// (e.g. when the configured LLM backend is unavailable or all models in a
// caller-defined fallback chain are exhausted). When LLMSkipped=true, the
// Answer is typically composed from top-N similar sentences rather than
// from an LLM summary, and Facts will usually be empty. Callers that
// require LLM-quality answers can branch on LLMSkipped to retry or surface
// the degraded state to their user. Both fields are omitempty so output
// shape is unchanged for the LLM-success path.
type SearchOutput struct {
	Query         string         `json:"query"`
	Answer        string         `json:"answer"`
	Facts         []llm.FactItem `json:"facts"`
	Sources       []SourceItem   `json:"sources"`
	LLMSkipped    bool           `json:"llm_skipped,omitempty"`
	DegradeReason string         `json:"degrade_reason,omitempty"`
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

// FormatOutput trims SearchOutput to fit within the given budget. The
// LLMSkipped/DegradeReason flags pass through unmodified so the caller-
// visible degrade state survives the trim.
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
func BuildSearchOutput(query string, llmOut *llm.StructuredOutput, results []sources.Result) SearchOutput {
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
