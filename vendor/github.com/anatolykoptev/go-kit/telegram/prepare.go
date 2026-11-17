package telegram

import (
	"html"
	"regexp"
	"strconv"
	"strings"
)

// PrepareForTelegram detects the markup format of text and returns a
// Telegram-ready (out, parseMode) pair. parseMode is always "HTML" —
// Telegram accepts HTML mode for plain-escaped text just as well.
//
// Routing:
//   - HTML input: SanitizeHTML → RepairHTMLNesting → ("HTML")
//   - Markdown input: MarkdownToHTML (converts + escapes + repairs) →
//     SanitizeHTML (defensive) → ("HTML")
//   - Plain input: EscapeHTML → ("HTML")
//   - Empty input: ("", "HTML")
func PrepareForTelegram(text string) (out string, parseMode string) {
	const mode = "HTML"
	text = SanitizeUTF8(text)
	if text == "" {
		return "", mode
	}
	if esc, ok := htmlRoute(text); ok {
		// Code spans are literal: hide them from the HTML parser (which
		// would swallow a `<` in them) and put them back as escaped code.
		masked, restore := protectCode(esc)
		// belt-and-braces: SanitizeHTML already walks the AST; RepairHTMLNesting catches any edge-case nesting the regex-free pass missed
		return restore(RepairHTMLNesting(SanitizeHTML(masked))), mode
	}
	switch Detect(text) {
	case FormatMarkdown:
		out = SanitizeHTML(MarkdownToHTML(text))
	default: // FormatPlain
		out = EscapeHTML(text)
	}
	return out, mode
}

var reCodeTagText = regexp.MustCompile(`<[^<>]*>`)

// protectCode swaps every fenced/inline code span for an opaque placeholder
// and returns a restore func that writes the spans back as escaped
// <pre><code>/<code> HTML. A placeholder that ended up inside a tag (an
// attribute value) is restored as attribute-escaped source text instead, so
// code can never inject markup into an attribute.
func protectCode(text string) (string, func(string) string) {
	spans := codeSpans(text)
	if len(spans) == 0 {
		return text, func(s string) string { return s }
	}
	// A delimiter absent from the input cannot be forged by it.
	open := "\uE000"
	for strings.Contains(text, open) {
		open += "\uE000"
	}
	nested := spansInCodeElement(text, spans)
	var b strings.Builder
	var htmlRepl, attrRepl []string
	last := 0
	for i, sp := range spans {
		if sp[0] < last {
			continue // defensive: overlapping span
		}
		src := text[sp[0]:sp[1]]
		ph := open + strconv.Itoa(i) + "\uE001"
		b.WriteString(text[last:sp[0]])
		b.WriteString(ph)
		last = sp[1]
		if nested[i] {
			// Already inside a <code>/<pre> element: keep the span literal.
			htmlRepl = append(htmlRepl, ph, EscapeHTML(src))
		} else {
			htmlRepl = append(htmlRepl, ph, codeSpanHTML(src))
		}
		attrRepl = append(attrRepl, ph, html.EscapeString(src))
	}
	b.WriteString(text[last:])
	htmlR, attrR := strings.NewReplacer(htmlRepl...), strings.NewReplacer(attrRepl...)
	return b.String(), func(s string) string {
		s = reCodeTagText.ReplaceAllStringFunc(s, attrR.Replace)
		return htmlR.Replace(s)
	}
}

// codeSpanHTML renders one code span (with its backticks) as Telegram HTML.
func codeSpanHTML(src string) string {
	if strings.HasPrefix(src, "```") {
		if m := reCodeBlock.FindStringSubmatch(src); len(m) >= codeBlockMatchGroups {
			if m[1] != "" {
				return `<pre><code class="language-` + m[1] + `">` + EscapeHTML(m[2]) + `</code></pre>`
			}
			return "<pre><code>" + EscapeHTML(m[2]) + "</code></pre>"
		}
	}
	return "<code>" + EscapeHTML(strings.Trim(src, "`")) + "</code>"
}

var reCodeElemTag = regexp.MustCompile(`(?i)<(/?)(?:code|pre)(?:\s[^<>]*)?>`)

// spansInCodeElement reports, per span, whether it sits inside an existing
// <code>/<pre> element of text (tags inside spans are literal and ignored).
func spansInCodeElement(text string, spans [][2]int) []bool {
	nested := make([]bool, len(spans))
	depth, si := 0, 0
	for _, loc := range reCodeElemTag.FindAllStringSubmatchIndex(text, -1) {
		for si < len(spans) && spans[si][0] < loc[0] {
			nested[si] = depth > 0
			si++
		}
		if si > 0 && loc[0] < spans[si-1][1] {
			continue // the tag is inside a code span
		}
		if loc[3] > loc[2] { // closing tag
			if depth > 0 {
				depth--
			}
		} else {
			depth++
		}
	}
	for ; si < len(spans); si++ {
		nested[si] = depth > 0
	}
	return nested
}
