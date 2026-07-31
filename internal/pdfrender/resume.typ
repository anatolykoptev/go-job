
#set page(
  paper:  "us-letter",
  margin: (x: 17.8mm, top: 17.8mm, bottom: 17.8mm),
)

// ligatures: false hardens ATS text extraction (fi/fl ligatures otherwise garble).
#set text(font: "IBM Plex Sans", size: 10pt, fill: rgb("#0f172a"), ligatures: false)
#show link: set text(fill: rgb("#26428b"))
// Vertical-rhythm cascade: line leading < item/paragraph spacing < section gap.
// Each bullet is a clear unit (item gap > line leading) so multi-line bullets read distinctly.
#set par(leading: 0.6em, spacing: 0.85em)
#set list(indent: 8pt, spacing: 0.78em)
#set enum(indent: 8pt, spacing: 0.78em)

// DEAD for resumes and cover letters, kept for any document that does emit a
// `# ` heading. The name is set by a raw typst block in the CONTENT, at a size
// this rule does not control — editing 19pt here moves nothing on a resume.
// The content owns the header; see DESIGN.md.
#show heading.where(level: 1): it => {
  v(3.5mm, weak: true)
  text(size: 19pt, weight: "bold", fill: rgb("#0f172a"), tracking: -0.4pt, it.body)
  v(1.6mm, weak: true)
  line(length: 100%, stroke: rgb("#cbd5e1") + 0.6pt)
  v(2.2mm, weak: true)
}
// Headings are "bold" (700), not "semibold" (600), and that is deliberate.
//
// In the image this service ships, family "IBM Plex Sans" carries weights
// 100/200/300/400/700 only — typst exposes 500 and 600 as SEPARATE families,
// "IBM Plex Sans Medm" and "IBM Plex Sans SmBld". Asking for 600 here resolved
// to 700 by nearest-weight, silently, so the approved layout has always been
// Bold. Naming 700 changes nothing on the page and stops the preamble asking for
// a face the image cannot supply.
//
// Verified by rendering a document that exercises every weight site in this
// file, including the table-header rule below, before and after: identical text,
// identical glyph geometry, identical font subsets. Reproduce it with any
// markdown covering h1-h4 plus a table; the claim is scoped to this image's font
// set, and a box carrying a variable-axis IBM Plex could resolve 600 differently.
//
// Wanting real SemiBold means naming "IBM Plex Sans SmBld" — the file already
// ships, the Dockerfile glob carries it, no image change — AND adding that
// family to requiredFontFamilies in adapter.go, or the preamble/required-list
// agreement test fails. It also moves the page, so it needs re-approval.
//
// Note what does NOT guard this: requiredFontFamilies and
// gojob_pdf_font_available check font FAMILIES, never weights. Reintroducing
// "semibold" would substitute silently again with no signal. This comment is the
// only thing standing there.
#show heading.where(level: 2): it => {
  v(4.5mm, weak: true)
  block(below: 3mm, breakable: false)[
    #text(size: 12pt, weight: "bold", fill: rgb("#334155"), tracking: 0.7pt, upper(it.body))
    #v(1mm, weak: true)
    #line(length: 100%, stroke: rgb("#cbd5e1") + 0.6pt)
  ]
}
#show heading.where(level: 3): it => {
  v(3.7mm, weak: true)
  text(size: 11pt, weight: "bold", fill: rgb("#0f172a"), it.body)
  v(3.5mm, weak: true)
}

// Entry subtitle: a one-line gloss of what the thing IS, set like the header's
// contact line (body size, slate-500) so it reads as metadata rather than content.
// NOTE: `above` is dominated and does nothing. The v(3.5mm, weak: true)
// closing the level-3 rule above wins, so the heading-to-subtitle gap is
// 6.67pt whatever this says — swept -20mm..+6mm, subtitle yMin identical to
// six decimals at every step. To move it, change that v(), not this.
#show heading.where(level: 4): it => block(above: -2.3mm, below: 3.2mm)[
  #text(size: 10pt, weight: "regular", fill: rgb("#64748b"), it.body)
]
#show raw.where(block: true): it => block(
  fill:   rgb("#f8fafc"),
  stroke: rgb("#e2e8f0") + 0.8pt,
  radius: 5pt,
  inset:  (x: 12pt, y: 10pt),
  width:  100%,
  text(font: "IBM Plex Mono", size: 9.5pt, fill: rgb("#334155"), it),
)
#show raw.where(block: false): it => text(font: "IBM Plex Mono", size: 9.5pt, fill: rgb("#1e293b"), it)

#show table: set table(stroke: (x, y) => {
  if y == 0 { (bottom: rgb("#94a3b8") + 1pt) }
  else { (bottom: rgb("#e2e8f0") + 0.6pt) }
})
#show table.cell.where(y: 0): set text(weight: "bold", size: 9pt)
#set table(inset: (x: 8pt, y: 6pt))

// ── cover (title page) injected by Go before body ────────
