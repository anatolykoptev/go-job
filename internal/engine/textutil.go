package engine

import (
	"regexp"
	"strings"

	"github.com/anatolykoptev/go-kit/strutil"
)

// NormLang normalises a language field: empty string → "all".
func NormLang(lang string) string {
	if lang == "" {
		return "all"
	}
	return lang
}

// User-Agent strings used across HTTP clients.
const (
	UserAgentBot    = "GoSearch/1.0"
	UserAgentChrome = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

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
	return strutil.TruncateWith(s, limit, suffix)
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
	return strutil.TruncateAtWord(s, maxLen)
}
