package extract

import (
	"regexp"
	"strings"
)

// Pre-compiled regexes for HTML content stripping.
// Package-level compilation prevents the per-call MustCompile regression
// present in go-job and go-startup.
var (
	reTitleTag = regexp.MustCompile(`(?i)<title[^>]*>([^<]+)</title>`)
	reOgTitle  = regexp.MustCompile(`(?i)<meta[^>]*property=["']og:title["'][^>]*content=["']([^"']+)["']`)
	reScript   = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	reStyle    = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	reNoscript = regexp.MustCompile(`(?is)<noscript[^>]*>.*?</noscript>`)
	reHeader   = regexp.MustCompile(`(?is)<header[^>]*>.*?</header>`)
	reFooter   = regexp.MustCompile(`(?is)<footer[^>]*>.*?</footer>`)
	reNav      = regexp.MustCompile(`(?is)<nav[^>]*>.*?</nav>`)
	reAside    = regexp.MustCompile(`(?is)<aside[^>]*>.*?</aside>`)
	reIframe   = regexp.MustCompile(`(?is)<iframe[^>]*>.*?</iframe>`)
	reHTMLTags = regexp.MustCompile(`<[^>]+>`)
)

// boilerplateRegexes are applied in order to strip non-content elements.
var boilerplateRegexes = []*regexp.Regexp{
	reScript, reStyle, reNoscript,
	reHeader, reFooter, reNav, reAside, reIframe,
}

// extractRegex uses regex-based HTML stripping as a last resort.
// Operates on already-fetched body — no re-fetching.
func (e *Extractor) extractRegex(body []byte) (*Result, error) {
	raw := string(body)

	// Extract title.
	var title string
	if m := reTitleTag.FindStringSubmatch(raw); len(m) > 1 {
		title = strings.TrimSpace(m[1])
	}
	if title == "" {
		if m := reOgTitle.FindStringSubmatch(raw); len(m) > 1 {
			title = strings.TrimSpace(m[1])
		}
	}

	// Strip boilerplate.
	for _, re := range boilerplateRegexes {
		raw = re.ReplaceAllString(raw, "")
	}

	// Strip remaining HTML tags.
	content := reHTMLTags.ReplaceAllString(raw, "")
	content = strings.TrimSpace(content)
	content = reWhitespace.ReplaceAllString(content, " ")
	content = cleanLines(content)

	return &Result{
		Title:   title,
		Content: e.truncate(content),
		Format:  FormatText, // regex always returns text
	}, nil
}
