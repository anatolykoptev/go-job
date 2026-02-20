package engine

import (
	"math/rand"
	"regexp"
	"strings"
	"time"
)

// User-Agent strings used across HTTP clients.
const (
	UserAgentBot    = "GoSearch/1.0"
	UserAgentChrome = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

// RandomUserAgents is a pool of Chrome-like User-Agents for rotation.
var RandomUserAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.2 Safari/605.1.15",
	"Mozilla/5.0 (X11; Linux x86_64; rv:109.0) Gecko/20100101 Firefox/115.0",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:109.0) Gecko/20100101 Firefox/115.0",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:109.0) Gecko/20100101 Firefox/115.0",
}

var uaRand = rand.New(rand.NewSource(time.Now().UnixNano()))

// RandomUserAgent returns a random Chrome-like User-Agent.
func RandomUserAgent() string {
	return RandomUserAgents[uaRand.Intn(len(RandomUserAgents))]
}

var htmlTagRe = regexp.MustCompile(`<[^>]+>`)

// CleanHTML strips HTML tags and trims whitespace.
func CleanHTML(s string) string {
	return strings.TrimSpace(htmlTagRe.ReplaceAllString(s, ""))
}

// Truncate returns the first n bytes of s.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// TruncateRunes caps s at limit runes, appending suffix if truncated.
// Pass suffix="" for no suffix. Safe for UTF-8 (Cyrillic, CJK, emoji).
func TruncateRunes(s string, limit int, suffix string) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + suffix
}

// CanonicalJobKey returns a normalized dedup key for cross-source job deduplication.
// Same job from LinkedIn + SearXNG + ATS will have the same key (same title, same location).
// Strips common company suffixes, normalizes whitespace, lowercases everything.
func CanonicalJobKey(title, location string) string {
	norm := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		// Strip " at CompanyName" suffix that LinkedIn prepends.
		if idx := strings.LastIndex(s, " at "); idx > 0 {
			s = s[:idx]
		}
		// Collapse all non-alpha-numeric chars to a single space.
		var b strings.Builder
		prevSpace := true
		for _, r := range s {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
				prevSpace = false
			} else if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		}
		return strings.TrimRight(b.String(), " ")
	}
	return norm(title) + "|" + norm(location)
}

// TruncateAtWord truncates a string to maxLen runes at a word boundary.
func TruncateAtWord(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	truncated := string(runes[:maxLen])
	cut := strings.LastIndex(truncated, " ")
	if cut < len(truncated)/2 {
		return truncated + "..."
	}
	return truncated[:cut] + "..."
}
