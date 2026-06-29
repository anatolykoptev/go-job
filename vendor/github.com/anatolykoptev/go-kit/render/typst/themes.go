package typst

// typstTheme holds the full Typst preamble for a named theme.
// The placeholder {{.Body}} is replaced at render time with the
// pandoc-converted content; {{.Title}} with the document title.
//
// All themes use IBM Plex Sans (installed on the server via fonts-ibm-plex).
// Letter dimensions are left to Typst's built-in "a4" paper preset.
type typstTheme struct {
	preamble string // everything before the body content
}

const (
	themeCard = "card"
	themeDark = "dark"
)

// resolveTypstTheme returns the theme preamble for the given name.
// Unknown names fall back to "report".
func resolveTypstTheme(name string) typstTheme {
	switch name {
	case "minimal":
		return typstThemeMinimal
	case "corporate":
		return typstThemeCorporate
	case themeCard:
		return typstThemeCard
	case themeDark:
		return typstThemeDark
	case "resume":
		return typstThemeResume
	default: // "report" and anything else
		return typstThemeReport
	}
}

// themePageMarginPt returns the margin (in pt) used when a caller supplies
// Width/Height pixel geometry. Card uses zero margin so the background
// fills edge-to-edge; dark gets a roomier 32pt; the rest match the
// 24pt body inset of the document themes.
func themePageMarginPt(theme string) float64 {
	switch theme {
	case themeCard:
		return 0
	case themeDark:
		return 32
	default:
		return 24
	}
}

// themeOmitsTitleBlock reports whether the Go-side title heading should be
// suppressed for a theme — card and dark render their own heading styling
// against the colored background and don't want a duplicate H1.
func themeOmitsTitleBlock(theme string) bool {
	return theme == themeCard || theme == themeDark
}

// ── report ────────────────────────────────────────────────
// Clean professional light theme.  Good for strategy memos, research
// briefs, and client deliverables.
var typstThemeReport = typstTheme{preamble: `
#set page(
  paper:  "a4",
  margin: (x: 24mm, top: 22mm, bottom: 26mm),
  header: context {
    if counter(page).get().first() > 1 {
      set text(size: 8pt, fill: rgb("#94a3b8"))
      grid(columns: (1fr, auto),
        [{{.Title}}],
        counter(page).display("1 / 1", both: true),
      )
      line(length: 100%, stroke: rgb("#e2e8f0") + 0.8pt)
    }
  },
  footer: context {
    set text(size: 7.5pt, fill: rgb("#cbd5e1"))
    align(center, datetime.today().display("[month repr:long] [year]"))
  },
)

#set text(font: "IBM Plex Sans", size: 10.5pt, fill: rgb("#0f172a"))
#set par(leading: 0.75em, spacing: 1.2em, justify: true)
#set list(indent: 8pt)
#set enum(indent: 8pt)

#show heading.where(level: 1): it => {
  v(8mm, weak: true)
  text(size: 21pt, weight: "semibold", fill: rgb("#0f172a"), tracking: -0.5pt, it.body)
  v(2mm, weak: true)
  line(length: 100%, stroke: rgb("#e2e8f0") + 1pt)
  v(4mm, weak: true)
}
#show heading.where(level: 2): it => {
  v(5mm, weak: true)
  text(size: 14pt, weight: "semibold", fill: rgb("#1e293b"), it.body)
  v(2mm, weak: true)
}
#show heading.where(level: 3): it => {
  v(3mm, weak: true)
  text(size: 11.5pt, weight: "semibold", fill: rgb("#334155"), it.body)
  v(1mm, weak: true)
}
#show raw.where(block: true): it => block(
  fill:   rgb("#f8fafc"),
  stroke: rgb("#e2e8f0") + 0.8pt,
  radius: 5pt,
  inset:  (x: 12pt, y: 10pt),
  width:  100%,
  text(font: "IBM Plex Mono", size: 9pt, fill: rgb("#334155"), it),
)
#show raw.where(block: false): it => box(fill: rgb("#f1f5f9"), inset: (x: 4pt, y: 2pt), radius: 3pt, text(font: "IBM Plex Mono", size: 9pt, fill: rgb("#1e293b"), it))

#show table: set table(stroke: (x, y) => {
  if y == 0 { (bottom: rgb("#94a3b8") + 1pt) }
  else { (bottom: rgb("#e2e8f0") + 0.6pt) }
})
#show table.cell.where(y: 0): set text(weight: "semibold", size: 9pt)
#set table(inset: (x: 8pt, y: 6pt))

// ── cover (title page) injected by Go before body ────────
`}

// ── minimal ───────────────────────────────────────────────
var typstThemeMinimal = typstTheme{preamble: `
#set page(paper: "a4", margin: (x: 32mm, top: 28mm, bottom: 28mm))
#set text(font: "IBM Plex Sans", size: 11pt, fill: rgb("#18181b"))
#set par(leading: 0.8em, spacing: 1.3em, justify: true)

#show heading.where(level: 1): it => {
  v(10mm, weak: true)
  text(size: 22pt, weight: "semibold", tracking: -0.5pt, it.body)
  v(6mm, weak: true)
}
#show heading.where(level: 2): it => {
  v(5mm, weak: true)
  text(size: 14pt, weight: "semibold", it.body)
  v(2mm, weak: true)
}
#show raw.where(block: true): it => block(
  fill: rgb("#fafafa"), stroke: rgb("#e4e4e7") + 0.8pt,
  radius: 4pt, inset: (x: 12pt, y: 10pt), width: 100%,
  text(font: "IBM Plex Mono", size: 9pt, it),
)
`}

// ── corporate ─────────────────────────────────────────────
var typstThemeCorporate = typstTheme{preamble: `
#let navy   = rgb("#1e3a5f")
#let accent = rgb("#2563eb")
#let border = rgb("#bfdbfe")

#set page(
  paper:  "a4",
  margin: (x: 22mm, top: 22mm, bottom: 26mm),
  header: context {
    if counter(page).get().first() > 1 {
      set text(size: 8pt, fill: navy)
      grid(columns: (1fr, auto),
        text(weight: "semibold")[{{.Title}}],
        counter(page).display("1"),
      )
      line(length: 100%, stroke: border + 1pt)
    }
  },
)
#set text(font: "IBM Plex Sans", size: 10.5pt, fill: rgb("#0f172a"))
#set par(leading: 0.72em, spacing: 1.15em, justify: true)

#show heading.where(level: 1): it => {
  v(8mm, weak: true)
  block(fill: navy, inset: (x: 12pt, y: 8pt), radius: 4pt, width: 100%,
    text(size: 18pt, weight: "semibold", fill: white, tracking: -0.3pt, it.body))
  v(4mm, weak: true)
}
#show heading.where(level: 2): it => {
  v(5mm, weak: true)
  stack(
    text(size: 13pt, weight: "semibold", fill: navy, it.body),
    v(2pt),
    line(length: 100%, stroke: border + 1.2pt),
  )
  v(3mm, weak: true)
}
#show raw.where(block: true): it => block(
  fill: rgb("#eff6ff"), stroke: border + 0.8pt,
  radius: 4pt, inset: (x: 12pt, y: 10pt), width: 100%,
  text(font: "IBM Plex Mono", size: 9pt, fill: navy, it),
)
`}

// ── card ──────────────────────────────────────────────────
// High-contrast white social-card aesthetic. No own page geometry —
// the Go-side override supplies width/height/margin in pixels.
var typstThemeCard = typstTheme{preamble: `
#set text(font: "IBM Plex Sans", size: 22pt, fill: rgb("#0F172A"))
#set par(leading: 0.9em, spacing: 1.4em)
#set align(center + horizon)

#show heading.where(level: 1): it => {
  set text(size: 56pt, weight: "bold", tracking: -1pt, fill: rgb("#0F172A"))
  block(width: 100%, it.body)
  v(8pt)
}
#show heading.where(level: 2): it => {
  set text(size: 32pt, weight: "semibold", fill: rgb("#1E293B"))
  block(width: 100%, it.body)
}
`}

// ── dark ──────────────────────────────────────────────────
// Dark social-story aesthetic. Same shape as card but inverted palette.
// Requires non-zero Width+Height in Options; otherwise output falls back
// to default A4 page size with theme styling.
var typstThemeDark = typstTheme{preamble: `
#set page(fill: rgb("#0E1117"))
#set text(font: "IBM Plex Sans", size: 22pt, fill: rgb("#F0F6FC"))
#set par(leading: 0.9em, spacing: 1.4em)
#set align(center + horizon)

#show heading.where(level: 1): it => {
  set text(size: 56pt, weight: "bold", tracking: -1pt, fill: rgb("#F0F6FC"))
  block(width: 100%, it.body)
  v(8pt)
}
#show heading.where(level: 2): it => {
  set text(size: 32pt, weight: "semibold", fill: rgb("#C9D1D9"))
  block(width: 100%, it.body)
}
`}

// ── resume ────────────────────────────────────────────────
// Compact single-page resume theme tuned for US job applications.
// US-Letter paper (recruiters print/scan Letter; A4 gets scaled or clipped).
// Left-aligned body (no justify) — justified text creates visible inter-word
// rivers that read as template tells.  Tighter margins, leading, and heading
// vspace than "report" so content-rich one-page CVs don't spill to a second
// page.  All show-rules for code blocks and tables are identical to "report".
var typstThemeResume = typstTheme{preamble: `
#set page(
  paper:  "us-letter",
  margin: (x: 20mm, top: 14mm, bottom: 14mm),
)

#set text(font: "IBM Plex Sans", size: 10.5pt, fill: rgb("#0f172a"))
#set par(leading: 0.6em, spacing: 0.7em)
#set list(indent: 8pt)
#set enum(indent: 8pt)

#show heading.where(level: 1): it => {
  v(3mm, weak: true)
  text(size: 16pt, weight: "semibold", fill: rgb("#0f172a"), tracking: -0.5pt, it.body)
  v(1mm, weak: true)
  line(length: 100%, stroke: rgb("#e2e8f0") + 1pt)
  v(1.5mm, weak: true)
}
#show heading.where(level: 2): it => {
  v(2.5mm, weak: true)
  text(size: 12pt, weight: "semibold", fill: rgb("#1e293b"), it.body)
  v(0.8mm, weak: true)
}
#show heading.where(level: 3): it => {
  v(2mm, weak: true)
  text(size: 10.5pt, weight: "semibold", fill: rgb("#334155"), it.body)
  v(0.5mm, weak: true)
}
#show raw.where(block: true): it => block(
  fill:   rgb("#f8fafc"),
  stroke: rgb("#e2e8f0") + 0.8pt,
  radius: 5pt,
  inset:  (x: 12pt, y: 10pt),
  width:  100%,
  text(font: "IBM Plex Mono", size: 9pt, fill: rgb("#334155"), it),
)
#show raw.where(block: false): it => box(fill: rgb("#f1f5f9"), inset: (x: 4pt, y: 2pt), radius: 3pt, text(font: "IBM Plex Mono", size: 9pt, fill: rgb("#1e293b"), it))

#show table: set table(stroke: (x, y) => {
  if y == 0 { (bottom: rgb("#94a3b8") + 1pt) }
  else { (bottom: rgb("#e2e8f0") + 0.6pt) }
})
#show table.cell.where(y: 0): set text(weight: "semibold", size: 9pt)
#set table(inset: (x: 8pt, y: 6pt))

// ── cover (title page) injected by Go before body ────────
`}
