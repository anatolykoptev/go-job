package typst

import "sync"

// Theme is a named Typst preamble plus the two document-level decisions that
// belong to a look rather than to a call site. Modelled on render/html.Theme,
// which carries only {Name, CSS} — the two extra fields here are decisions the
// Typst pipeline makes per document that CSS expresses inline.
//
// Preamble is parsed as a text/template with {{.Title}} available; write a
// literal brace pair as {{"{{"}}. There is no {{.Body}} placeholder — the
// converted body is concatenated after the preamble, not substituted into it.
//
// The built-in themes use IBM Plex Sans, which must be present wherever typst
// runs: a missing family is substituted silently, so the document renders in
// whatever typst falls back to rather than failing.
type Theme struct {
	// Name selects the theme via render.Options.Theme. Must be non-empty:
	// RegisterTheme panics on a blank name, because the resolver treats a blank
	// request as "report" and such an entry would be permanently unreachable.
	Name string
	// Preamble is everything before the body content.
	Preamble string
	// PageMarginPt is the margin used when a caller supplies Width/Height pixel
	// geometry for image output. Zero is meaningful (edge-to-edge), so a theme
	// that wants the shared default states 24 explicitly.
	PageMarginPt float64
	// OmitsTitleBlockOnImage suppresses the Go-side title heading on the IMAGE
	// path only. The name is that specific because the built-ins that set it,
	// card and dark, do not render a title themselves — they only style a
	// level-1 heading the body is expected to supply. Social cards carry their
	// heading in the body, so injecting one duplicates it; a PDF of the same
	// content does not, so suppressing there loses the title outright with no
	// error.
	//
	// There is deliberately no PDF-path equivalent yet. A consumer theme that
	// emits its own masthead from {{.Title}} currently has no way to avoid the
	// injected heading on a PDF: with Title set it gets both, with Title empty
	// the masthead renders blank. Suppressing there needs its own opt-in rather
	// than reusing this flag, whose built-in users emit no title at all.
	OmitsTitleBlockOnImage bool
}

const (
	themeCard    = "card"
	themeDark    = "dark"
	themeDefault = "report"
)

var (
	themeMu  sync.RWMutex
	themeReg = map[string]Theme{}
)

// RegisterTheme adds a theme to the registry. Safe to call from init functions.
// A later call with the same name overwrites the earlier one, so a consumer may
// replace a built-in as well as add its own — note that replacing "report" also
// replaces what every unknown name falls back to.
//
// This is how a product owns a house style without it living in the shared theme
// set: register from the product's init, then select it by name exactly like a
// built-in. Registration is expected at init time; the registry is safe to write
// later, but a render already in flight resolves its theme once at entry.
//
// Panics on a blank Name — see Theme.Name. This is stricter than
// render/html.RegisterTheme, which stores such an entry unreachably; do not
// call it with caller-controlled data.
func RegisterTheme(t Theme) {
	if t.Name == "" {
		panic("render/typst: RegisterTheme called with a blank Name")
	}
	themeMu.Lock()
	themeReg[t.Name] = t
	themeMu.Unlock()
}

// LookupTheme returns the registered theme for a name and whether it was found.
// Use it to build a variant on top of a built-in instead of copying its preamble
// out of this package and owning the drift.
//
// Unlike render/html.LookupTheme, which silently substitutes "report" on a miss,
// this reports the miss — a caller composing on top of a theme needs to know it
// got a different one. resolveTypstTheme is where the render-time fallback lives.
func LookupTheme(name string) (Theme, bool) {
	themeMu.RLock()
	defer themeMu.RUnlock()
	t, ok := themeReg[name]
	return t, ok
}

// resolveTypstTheme returns the theme for the given name, falling back to
// "report" when the name is unknown or empty. A blank name misses the map and
// takes the same fallback, so it needs no special case.
//
// Returns the zero Theme only if "report" is absent from the registry. init()
// registers it, so that requires a consumer to have overwritten it — with an
// empty Preamble, that silently becomes the fallback for every unknown name.
func resolveTypstTheme(name string) Theme {
	themeMu.RLock()
	defer themeMu.RUnlock()
	if t, ok := themeReg[name]; ok {
		return t
	}
	return themeReg[themeDefault]
}

// Card uses zero margin so its background fills edge-to-edge; dark gets a
// roomier 32pt; the document themes match their 24pt body inset.
//
// Card and dark suppress the injected title on the image path because a social
// card supplies its own heading in the body — neither preamble emits a title,
// they only style a level-1 heading. That is why the flag is image-only: on the
// PDF path the same suppression would drop the title with nothing replacing it.
func init() {
	RegisterTheme(Theme{Name: themeDefault, Preamble: typstThemeReport, PageMarginPt: 24})
	RegisterTheme(Theme{Name: "minimal", Preamble: typstThemeMinimal, PageMarginPt: 24})
	RegisterTheme(Theme{Name: "corporate", Preamble: typstThemeCorporate, PageMarginPt: 24})
	RegisterTheme(Theme{Name: themeCard, Preamble: typstThemeCard, PageMarginPt: 0, OmitsTitleBlockOnImage: true})
	RegisterTheme(Theme{Name: themeDark, Preamble: typstThemeDark, PageMarginPt: 32, OmitsTitleBlockOnImage: true})
	RegisterTheme(Theme{Name: "resume", Preamble: typstThemeResume, PageMarginPt: 24})
}

// ── report ────────────────────────────────────────────────
// Clean professional light theme.  Good for strategy memos, research
// briefs, and client deliverables.
var typstThemeReport = `
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
`

// ── minimal ───────────────────────────────────────────────
var typstThemeMinimal = `
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
`

// ── corporate ─────────────────────────────────────────────
var typstThemeCorporate = `
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
`

// ── card ──────────────────────────────────────────────────
// High-contrast white social-card aesthetic. No own page geometry —
// the Go-side override supplies width/height/margin in pixels.
var typstThemeCard = `
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
`

// ── dark ──────────────────────────────────────────────────
// Dark social-story aesthetic. Same shape as card but inverted palette.
// Requires non-zero Width+Height in Options; otherwise output falls back
// to default A4 page size with theme styling.
var typstThemeDark = `
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
`

// ── resume ────────────────────────────────────────────────
// Compact single-page resume theme tuned for US job applications.
// US-Letter paper (recruiters print/scan Letter; A4 gets scaled or clipped).
// Left-aligned body (no justify) — justified text creates visible inter-word
// rivers that read as template tells.  Tighter margins, leading, and heading
// vspace than "report" so content-rich one-page CVs don't spill to a second
// page.  All show-rules for code blocks and tables are identical to "report".
var typstThemeResume = `
#set page(
  paper:  "us-letter",
  margin: (x: 16mm, top: 14mm, bottom: 14mm),
)

// ligatures: false hardens ATS text extraction (fi/fl ligatures otherwise garble).
#set text(font: "IBM Plex Sans", size: 10.5pt, fill: rgb("#0f172a"), ligatures: false)
#show link: set text(fill: rgb("#26428b"))
// Vertical-rhythm cascade: line leading < item/paragraph spacing < section gap.
// Each bullet is a clear unit (item gap > line leading) so multi-line bullets read distinctly.
#set par(leading: 0.68em, spacing: 0.85em)
#set list(indent: 8pt, spacing: 0.9em)
#set enum(indent: 8pt, spacing: 0.9em)

#show heading.where(level: 1): it => {
  v(3.5mm, weak: true)
  text(size: 19pt, weight: "bold", fill: rgb("#0f172a"), tracking: -0.4pt, it.body)
  v(1.6mm, weak: true)
  line(length: 100%, stroke: rgb("#cbd5e1") + 0.6pt)
  v(2.2mm, weak: true)
}
#show heading.where(level: 2): it => {
  v(3.8mm, weak: true)
  block(below: 1.5mm, breakable: false)[
    #text(size: 11.5pt, weight: "semibold", fill: rgb("#334155"), tracking: 0.4pt, upper(it.body))
    #v(1mm, weak: true)
    #line(length: 100%, stroke: rgb("#e2e8f0") + 0.6pt)
  ]
}
#show heading.where(level: 3): it => {
  v(3mm, weak: true)
  text(size: 10.5pt, weight: "semibold", fill: rgb("#334155"), it.body)
  v(1.2mm, weak: true)
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
`
