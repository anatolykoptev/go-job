package telegram

import (
	"regexp"
	"sort"
	"strings"
)

// Format represents the detected markup format of a text string.
type Format int

const (
	// FormatPlain indicates plain text with no markup.
	FormatPlain Format = iota
	// FormatHTML indicates HTML markup (Telegram-compatible or raw HTML).
	FormatHTML
	// FormatMarkdown indicates Markdown markup.
	FormatMarkdown
)

// String returns a human-readable name for the format.
func (f Format) String() string {
	switch f {
	case FormatHTML:
		return "HTML"
	case FormatMarkdown:
		return "Markdown"
	default:
		return "Plain"
	}
}

// fieldSet builds a set from a space-separated list of names.
func fieldSet(names string) map[string]bool {
	set := map[string]bool{}
	for _, n := range strings.Fields(names) {
		set[n] = true
	}
	return set
}

// htmlTagNames is the whitelist of tags that route text to the HTML path.
// Every name here is handled by SanitizeHTML (kept, renamed or flattened).
var htmlTagNames = fieldSet("b strong i em u ins s strike del a code pre blockquote " +
	"span p div br ul ol li tg-spoiler tg-emoji h1 h2 h3 h4 h5 h6")

// htmlVoidTags never take a closing tag, so they do not count toward balance.
var htmlVoidTags = fieldSet("br")

// htmlNoAttrTags are pure formatting tags that must not carry attributes; a
// match like `<b and c>` is comparison text, not markup.
var htmlNoAttrTags = fieldSet("b strong i em u ins s strike del tg-spoiler br")

// reHTMLTagAt matches one well-formed tag anchored at the start of the input:
// `<name attr="v" flag>`, `</name>` or `<name/>`.
var reHTMLTagAt = regexp.MustCompile(
	`^<(/?)([A-Za-z][A-Za-z0-9-]*)((?:\s+[A-Za-z][A-Za-z0-9_:-]*(?:\s*=\s*(?:"[^"<>]*"|'[^'<>]*'))?)*)\s*(/?)>`,
)

// htmlImplicitTags are closed implicitly by HTML (`<p>one<p>two`,
// `<ul><li>a<li>b</ul>`), so they never need a matching closer.
var htmlImplicitTags = fieldSet("p li")

var (
	reDetectFence  = regexp.MustCompile("(?s)```.*?```")
	reDetectInline = regexp.MustCompile("`[^`\\n]+`")
)

// maskTagBackticks returns text with every backtick that sits inside a
// well-formed tag (`<a href="`">`) overwritten by 'x', so it can never act as
// a code delimiter. Offsets are preserved.
func maskTagBackticks(text string) string {
	if !strings.Contains(text, "`") {
		return text
	}
	var b []byte
	for i := 0; i < len(text); i++ {
		if !isTagLikeStart(text, i) {
			continue
		}
		tag := reHTMLTagAt.FindString(text[i:])
		if tag == "" {
			continue
		}
		if strings.Contains(tag, "`") {
			if b == nil {
				b = []byte(text)
			}
			for k := i; k < i+len(tag); k++ {
				if b[k] == '`' {
					b[k] = 'x'
				}
			}
		}
		i += len(tag) - 1
	}
	if b == nil {
		return text
	}
	return string(b)
}

// codeSpans returns the non-overlapping byte ranges of fenced and inline
// code in text, in order. Their content is literal and never HTML. Backticks
// inside tags are not delimiters, and an inline span never crosses a fence.
func codeSpans(text string) [][2]int {
	scan := maskTagBackticks(text)
	var spans [][2]int
	masked := []byte(scan)
	for _, loc := range reDetectFence.FindAllStringIndex(scan, -1) {
		spans = append(spans, [2]int{loc[0], loc[1]})
		for i := loc[0]; i < loc[1]; i++ {
			masked[i] = '\n' // inline spans cannot contain a newline
		}
	}
	for _, loc := range reDetectInline.FindAllStringIndex(string(masked), -1) {
		spans = append(spans, [2]int{loc[0], loc[1]})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
	out := spans[:0]
	for _, sp := range spans {
		if len(out) > 0 && sp[0] < out[len(out)-1][1] {
			continue // defensive: callers rely on disjoint, ordered spans
		}
		out = append(out, sp)
	}
	return out
}

// maskCode returns text with every code span overwritten by 'x' (same byte
// length, so offsets still line up with the original).
func maskCode(text string) string {
	spans := codeSpans(text)
	if len(spans) == 0 {
		return text
	}
	b := []byte(text)
	for _, sp := range spans {
		for i := sp[0]; i < sp[1]; i++ {
			b[i] = 'x'
		}
	}
	return string(b)
}

// isTagLikeStart reports whether text[i:] begins a tag-like token: `<`
// followed by a letter, `/`, `!` or `?` (comments, CDATA, processing
// instructions). Comparisons (`a < b`, `x<5`, `<-`) are not.
func isTagLikeStart(text string, i int) bool {
	if text[i] != '<' || i+1 >= len(text) {
		return false
	}
	c := text[i+1]
	return c == '/' || c == '!' || c == '?' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// htmlTok is one tag-like token found by htmlRoute.
type htmlTok struct {
	pos     int // byte offset of '<'
	name    string
	closing bool
	bad     bool // not a whitelisted well-formed tag, or unbalanced
}

// validTag reports whether the matched tag is whitelisted and well-formed.
func validTag(m []string) bool {
	closing, name, attrs, selfClose := m[1] == "/", strings.ToLower(m[2]), m[3], m[4] == "/"
	if !htmlTagNames[name] || (closing && (attrs != "" || selfClose)) {
		return false
	}
	return attrs == "" || !htmlNoAttrTags[name]
}

// pairTags marks unbalanced tokens bad. A closer matches the nearest open
// tag of the same name; p/li, void and self-closing tags need no partner.
func pairTags(toks []htmlTok, selfClosed map[int]bool) {
	var stack []int
	for i := range toks {
		t := &toks[i]
		if t.bad || htmlVoidTags[t.name] || htmlImplicitTags[t.name] || selfClosed[i] {
			continue
		}
		if !t.closing {
			stack = append(stack, i)
			continue
		}
		matched := false
		for k := len(stack) - 1; k >= 0; k-- {
			if toks[stack[k]].name == t.name {
				stack = append(stack[:k], stack[k+1:]...)
				matched = true
				break
			}
		}
		t.bad = !matched
	}
	for _, i := range stack {
		toks[i].bad = true
	}
}

// htmlRoute decides whether text takes the HTML path. Tag-like tokens that
// are not whitelisted, well-formed and balanced (`a<b`, `List<int>`,
// `<script>`, a stray `</b>`) are made literal by rewriting their `<` to
// `&lt;`; the returned string is the text to feed SanitizeHTML. ok is true
// when at least one genuine tag remains. Code spans are never touched.
func htmlRoute(text string) (escaped string, ok bool) {
	masked := maskCode(text)
	var toks []htmlTok
	selfClosed := map[int]bool{}
	for i := 0; i < len(masked); i++ {
		if !isTagLikeStart(masked, i) {
			continue
		}
		m := reHTMLTagAt.FindStringSubmatch(masked[i:])
		if m == nil {
			toks = append(toks, htmlTok{pos: i, bad: true})
			continue
		}
		toks = append(toks, htmlTok{pos: i, name: strings.ToLower(m[2]), closing: m[1] == "/", bad: !validTag(m)})
		if m[4] == "/" {
			selfClosed[len(toks)-1] = true
		}
		i += len(m[0]) - 1
	}
	pairTags(toks, selfClosed)
	var b strings.Builder
	last := 0
	for _, t := range toks {
		if !t.bad {
			ok = true
			continue
		}
		b.WriteString(text[last:t.pos])
		b.WriteString("&lt;")
		last = t.pos + 1
	}
	if !ok {
		return text, false
	}
	b.WriteString(text[last:])
	return b.String(), true
}

// reMDHeuristics holds individual markdown signals, each sufficient for detection.
//
// Note: in Go regex \w is ASCII-only ([0-9A-Za-z_]), so the bold heuristics use
// [\p{L}\p{N}_] instead — this also matches non-ASCII letters (Cyrillic, CJK, …)
// so that **Жирный** / __жирный__ / **日本語** are detected as markdown rather
// than misclassified as plain text and sent unformatted with literal ** / __.
var reMDHeuristics = []*regexp.Regexp{
	regexp.MustCompile(`\*\*[\p{L}\p{N}_]`),   // **word (Unicode-aware)
	regexp.MustCompile(`__[\p{L}\p{N}_]`),     // __word (Unicode-aware)
	regexp.MustCompile(`\[[^\]]+\]\([^)]+\)`), // [text](url)
	regexp.MustCompile("(?m)^```"),            // fenced code block
	regexp.MustCompile(`(?m)^# `),             // ATX heading
	regexp.MustCompile(`(?m)^- `),             // list item
}

// Detect heuristically identifies the markup format of text.
// Returns FormatHTML when the text contains at least one well-formed,
// whitelisted, balanced HTML tag (see htmlRoute); text that merely contains
// `<b`, `<script>` or `a<b` falls through to Markdown/plain.
// Returns FormatMarkdown if Markdown patterns are found.
// Returns FormatPlain otherwise.
func Detect(text string) Format {
	if _, ok := htmlRoute(text); ok {
		return FormatHTML
	}
	for _, re := range reMDHeuristics {
		if re.MatchString(text) {
			return FormatMarkdown
		}
	}
	return FormatPlain
}
