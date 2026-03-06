package search

import (
	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-stealth/websearch"
)

// ResultsToMarkdown formats search results as numbered markdown for LLM consumption.
// Delegates to websearch.ResultsToMarkdown.
func ResultsToMarkdown(results []sources.Result) string {
	return websearch.ResultsToMarkdown(sourceToWSResults(results))
}
