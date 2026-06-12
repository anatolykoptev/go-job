// Package webtext provides text cleaning and truncation utilities
// for web content processing.
package webtext

import (
	"regexp"
	"strings"
)

var (
	reHTMLTag    = regexp.MustCompile(`<[^>]*>`)
	reMultiSpace = regexp.MustCompile(`\s{2,}`)
	reEmptyLines = regexp.MustCompile(`\n\s*\n`)
)

// CleanHTML strips HTML tags and trims whitespace.
func CleanHTML(s string) string {
	return strings.TrimSpace(reHTMLTag.ReplaceAllString(s, ""))
}

// CleanLines removes empty lines and normalizes whitespace.
func CleanLines(s string) string {
	s = reEmptyLines.ReplaceAllString(s, "\n")
	lines := strings.Split(s, "\n")
	var out []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// NormalizeSpaces collapses multiple whitespace chars into a single space.
func NormalizeSpaces(s string) string {
	return strings.TrimSpace(reMultiSpace.ReplaceAllString(s, " "))
}
