
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

#show heading.where(level: 1): it => {
  v(3.5mm, weak: true)
  text(size: 19pt, weight: "bold", fill: rgb("#0f172a"), tracking: -0.4pt, it.body)
  v(1.6mm, weak: true)
  line(length: 100%, stroke: rgb("#cbd5e1") + 0.6pt)
  v(2.2mm, weak: true)
}
#show heading.where(level: 2): it => {
  v(4.5mm, weak: true)
  block(below: 3mm, breakable: false)[
    #text(size: 12pt, weight: "semibold", fill: rgb("#334155"), tracking: 0.7pt, upper(it.body))
    #v(1mm, weak: true)
    #line(length: 100%, stroke: rgb("#e2e8f0") + 0.6pt)
  ]
}
#show heading.where(level: 3): it => {
  v(3.7mm, weak: true)
  text(size: 11pt, weight: "semibold", fill: rgb("#334155"), it.body)
  v(3.5mm, weak: true)
}

// Entry subtitle: a one-line gloss of what the thing IS, set like the header's
// contact line (body size, slate-500) so it reads as metadata rather than content.
// The negative `above` pulls it back under its own heading: level 3 leaves 3.5mm for
// the bullets that normally follow, and an entry that has no subtitle keeps that gap.
#show heading.where(level: 4): it => block(above: -2.3mm, below: 3.2mm)[
  #text(size: 10pt, weight: "regular", fill: rgb("#64748b"), it.body)
]
#show raw.where(block: true): it => block(
  fill:   rgb("#f8fafc"),
  stroke: rgb("#e2e8f0") + 0.8pt,
  radius: 5pt,
  inset:  (x: 12pt, y: 10pt),
  width:  100%,
  text(font: "IBM Plex Mono", size: 9pt, fill: rgb("#334155"), it),
)
#show raw.where(block: false): it => text(font: "IBM Plex Mono", size: 9pt, fill: rgb("#1e293b"), it)

#show table: set table(stroke: (x, y) => {
  if y == 0 { (bottom: rgb("#94a3b8") + 1pt) }
  else { (bottom: rgb("#e2e8f0") + 0.6pt) }
})
#show table.cell.where(y: 0): set text(weight: "semibold", size: 9pt)
#set table(inset: (x: 8pt, y: 6pt))

// ── cover (title page) injected by Go before body ────────
