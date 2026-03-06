package websearch

import (
	"fmt"
	"strings"
)

// ResultsToMarkdown formats search results as numbered markdown for LLM consumption.
func ResultsToMarkdown(results []Result) string {
	if len(results) == 0 {
		return ""
	}

	var sb strings.Builder
	for i, r := range results {
		if i > 0 {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "## %d. [%s](%s)", i+1, r.Title, r.URL)
		if r.Content != "" {
			fmt.Fprintf(&sb, "\n%s", r.Content)
		}
	}
	return sb.String()
}
