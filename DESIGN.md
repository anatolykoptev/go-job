# go-job Documents — Design System

**Surface**: the PDFs `internal/pdfrender` renders — `resume.pdf` and `cover.pdf`, written per application under `$UPLOADS_ROOT/go-job/applications/<job_id>/`.

**Not this surface**: the operator admin UI under `internal/adminui`. It is a different medium with a different audience and has no entry here; do not read this file as governing it.

**Register**: document. A printed-or-screen-read artifact that must survive machine extraction, not an interface.

## Audience

Two readers, in this order, and they want opposite things.

A **recruiter or hiring manager** skims. They read the top third, decide, and move on; the document competes with a stack of others. Scanability and hierarchy win over density.

An **ATS parser** extracts text before any human sees it. It has no opinion about typography and every opinion about glyph encoding. A layout choice that breaks extraction removes the document from consideration without anyone reading it. This is why `ligatures: false` is set at `resume.typ:8` — an `fi` ligature extracts as a private-use codepoint and garbles the word.

Where the two conflict, the parser wins, because it decides whether the human ever reads the page.

## Provenance

The scale and rhythm derive from **rendercv**'s `engineeringresumes` theme defaults — a project of comparable scale that renders resumes through the same Typst engine, so its numbers are a measured reference rather than a preference. Reference defaults: body 10pt, name 30pt, section titles 1.4em bold, margins 0.7in on all four sides, line spacing 0.6em, 0.5cm above a section and 0.3cm below, 1.2em between entries, 0cm between bullet items.

Four deviations from that reference are deliberate. Each is listed with its reason in **Deliberate deviations** below. Anything not listed there is not a deviation — it is the reference, and changing it needs the same evidence the reference has.

## Documents

Both go through the same path: markdown → `pandoc -t typst` → theme preamble prepended → `typst compile`, at `adapter.go:274`. Both request `Theme: "resume"` at `adapter.go:293`. There is no second theme in use; go-kit ships `report`, `minimal`, `corporate`, `card` and `dark`, and go-job registers its own `resume` over go-kit's built-in at `adapter.go:48`.

| Document | Shape |
|---|---|
| `resume.pdf` | Raw-typst header, then `##` sections of `###`/`####` entries with bullets. Two pages is normal and expected for a senior candidate; one is not a target. |
| `cover.pdf` | Same theme, prose only. No headings, no bullets, no entries — so most of the theme's rules do not fire, by design. |

## Tokens

### Type scale

Values are read from `internal/pdfrender/resume.typ` — treat that file as authoritative and this table as its documentation.

| Role | Size | Weight | Where |
|---|---|---|---|
| Name | 26pt | bold | content header block, not the theme |
| Section (`##`) | 12pt | bold, `tracking: 0.7pt`, uppercased | `resume.typ:50` |
| Entry (`###`) | 11pt | bold | `resume.typ:57` |
| Entry subtitle (`####`) | 10pt | regular | `resume.typ:66` |
| Body | 10pt | regular | `resume.typ:8` |
| Inline + block code | 9pt | regular, IBM Plex Mono | `resume.typ:68` (block), `:76` (inline) |
| Table header row | 9pt | bold | `resume.typ:82` |

The level-1 heading rule (19pt) exists at `resume.typ:18` but never fires in a resume: the name is set by a raw typst block in the content, so no `#` heading is emitted. It is kept for documents that do use one, and `reference_test.go` records that decision rather than letting it look like an oversight.

### Colour

Single neutral ramp, no accent. Deliberate: an infrastructure resume earns attention with content, and a colour accent on a document that may be printed in greyscale buys nothing.

| Value | Contrast on white | Role |
|---|---|---|
| `#0f172a` | 17.96:1 | body text, name |
| `#1e293b` | 14.6:1 | inline code, header tagline |
| `#334155` | 10.34:1 | section and entry headings, code blocks |
| `#64748b` | 4.76:1 | entry subtitle, contact line — metadata register (AA pass, AAA fail) |
| `#26428b` | 8.5:1 | links — currently unreachable, see **Known gaps** |
| `#cbd5e1` | 1.50:1 | rule under the name, 0.7pt |
| `#e2e8f0` | 1.23:1 | rule under a section heading, 0.6pt; table row rules |
| `#94a3b8` | — | table header underline, 1pt |
| `#f8fafc` | — | code block fill |

**The ink hierarchy is currently inverted and that is a defect, not a choice.** Headings sit at `#334155` while the body they head sits at `#0f172a`, so the darkest mass on the page is running prose and the entry titles a reader scans for recede behind the sentences beneath them. Recorded in **Known gaps**; the intended value for an entry heading is `#0f172a`.

### Page

US Letter, 612×792pt. Margins 17.8mm (0.7in) on all four sides, giving a **511.08pt** measure (612 − 2×50.46).

That measure is wide by book typography — the current resume runs 37 of 86 body lines past 90 characters against a comfortable band of 45–75 — but it is the reference's own measure for this genre, and narrowing it to ~90 characters would need ~1.2in side margins and overflow the page. Keep 511pt; the actionable defect is the short wrap tails, not the measure.

## Vertical rhythm

Every gap is set against the one beneath it, so each level reads as a level. The gap below a heading stays smaller than the gap above it, so a heading binds to its own content instead of floating between groups.

| Level | Directive | Measured ink-to-ink |
|---|---|---|
| Lines inside one bullet | `leading: 0.6em` | 3.0pt |
| Between bullets | `list spacing: 0.78em` | 4.8pt |
| Between blocks | `par spacing: 0.85em` | not measured separately |
| Between two entries | `v(3.7mm)` above an entry | 6.0–7.4pt |
| Between two sections | `v(4.5mm)` above a section | 9.5pt |

Directive values and ink-to-ink measurements differ because a text box starts at cap height and ends below the baseline; the ordering is what matters. Verify with `pdftotext -bbox` rather than by eye.

Paragraph spacing is listed for completeness but was not isolated in that measurement — on this content it rarely occurs between two bare paragraphs, so its own gap did not separate cleanly from the entry and section gaps around it.

**The cascade holds at the coarse levels and breaks inside the entry tier.** Measured across all six boundaries:

| Boundary | Ink gap |
|---|---|
| Within a bullet | 2.98pt |
| Between bullets | 4.78pt |
| Subtitle → its first bullet | 6.05pt |
| Entry heading → its subtitle | 6.68pt |
| Entry → entry | 7.39pt |
| Section → section | 9.57pt |

Two problems, both against this document's own stated principle that *a heading binds to its own content, so the gap below it stays smaller than the gap above it*. The entry subtitle has **6.68pt above and 6.05pt below** — it floats between its heading and its bullets rather than binding upward; the `above: -2.3mm` pull-back is about 1mm short. And the entry boundary at 7.39pt is only 10% larger than an intra-entry gap, which is invisible at reading distance, so an entry boundary is carried by its bold heading alone rather than by space.

Intended values, once there is vertical budget for them: subtitle `above: -3.3mm`, entry `v(4.6mm)`, section `v(5.6mm)` — a 3.0 / 4.8 / 4.0 / 6.0 / 9.0 / 12.6 cascade. Cost is roughly 37pt against 2.36pt of current slack, so this cannot ship before the space is recovered. The subtitle fix alone is 1mm and costs nothing; take it first.

## Authoring contract

**This is the part that has actually failed.** The theme is a set of `#show` rules, and a rule only reaches the page if the markdown carries the construct that triggers it. Between 2026-06-27 and 2026-07-31 the entry-subtitle rule fired zero times because every entry had been written as a single `###` line. Nothing reported a fault — the PDF rendered, downloaded and opened.

Author a resume in this shape. The canonical example is `internal/pdfrender/testdata/resume-reference.md`; copy its structure, not its content.

````markdown
```{=typst}
#text(size: 26pt, weight: "bold", fill: rgb("#0f172a"), tracking: -0.4pt)[Jordan Avery]
#v(1.6mm)
#line(length: 100%, stroke: rgb("#cbd5e1") + 0.7pt)
#v(2.4mm)
#text(size: 11pt, weight: "semibold", fill: rgb("#1e293b"))[Platform Engineer  ·  Storage, Scheduling, Go]
#linebreak()
#v(0.8mm)
#text(size: 10pt, fill: rgb("#64748b"))[Portland, OR  ·  jordan\@example.invalid  ·  github.com/example]
```

## Section Title

### Entry title · MIT · v3.4.0 · 18.2K LOC · 210 commits

#### What the thing IS · github.com/example/example-cache

- A bullet. One claim, one line where possible.
````

Rules that follow from that:

- `##` opens a section. `###` is an entry title — the project or the job. `####` is the entry's one-line subtitle, the gloss of what the thing *is*, and it is a distinct level rather than a suffix on the `###` line. Collapsing the two is the failure described above.
- Inline code uses backticks and renders in Plex Mono; use it for identifiers, never for emphasis.
- A table needs its delimiter row (`| --- |`); the header row above it is styled separately.
- The name, tagline and contact line live in the content's raw-typst block, not in the theme. That is why they can drift, and why a change there is a design decision rather than a content edit.

## Hard constraints

### Only these weights exist

The image installs `fonts-ibm-plex`, whose `IBM Plex Sans` family carries **100/200/300/400/700**. Typst exposes 500 and 600 as *separate families* — `IBM Plex Sans Medm` and `IBM Plex Sans SmBld`.

Asking the plain family for `weight: "semibold"` therefore resolves to **700 Bold** by nearest-weight, silently. No warning, no error, output that looks deliberate. Measured with a width ladder: 400 and 500 both 224.826pt, 600 and 700 both 233.046pt, and an explicit `SmBld` 230.646pt.

Consequence for this document: name only weights the family has. Wanting real SemiBold means naming `"IBM Plex Sans SmBld"` *and* adding that family to `requiredFontFamilies` in `adapter.go`, or the preamble/required-list agreement test fails.

Nothing detects a violation. `gojob_pdf_font_available` checks font **families**, never weights.

### Fonts must be in the image

The theme names IBM Plex Sans and IBM Plex Mono. Typst substitutes a missing family **without erroring** — for five weeks every resume rendered in Libertinus Serif while the renderer gauge read healthy. The Dockerfile's `fonts` stage copies the TTFs from an `ubuntu:24.04` stage; `gojob_pdf_font_available` and the `GojobPdfFontMissing` alert exist because that failure has no other symptom.

### ATS

`ligatures: false` is non-negotiable — an `fi` ligature extracts as a private-use codepoint and garbles the word. The PDF is tagged, `/Lang` is set, and all 819 words round-trip cleanly through `pdftotext`.

`≈` is a *voice* issue rather than an extraction one: U+2248 round-trips fine under poppler. It reads as engineer shorthand in recruiter-facing prose, and the document currently marks approximation three different ways (`≈51K`, `1830x`, `230+`). Write "about" in prose.

Adding links does not harm extraction: `/URI` annotations are ignored by text extractors, and keeping link text identical to the visible string leaves the plaintext byte-identical.

## What is gated, and what is not

`internal/pdfrender/reference_test.go` holds two halves of the theme contract:

- every `#show` rule is either exercised by the reference fixture or recorded as knowingly unexercised with a reason, so a rule cannot enter the theme uncovered;
- the entry-subtitle rule is measured on a real render, as a ratio against a control run so the assertion survives a toolchain change.

**Neither gates the markdown that ships.** The document `PDF()` renders is supplied by its caller and never read from the fixture, so a resume authored flat still renders with the level-4 rule firing zero times and both tests green. Tracked in #409.

CI pins typst 0.14.2 and fonts-ibm-plex 6.1.1-1; the runtime image pins neither. Tracked in #410.

## Known gaps

Findings from design review of the shipped output, recorded rather than fixed so the next reader does not re-derive them. Ranked by impact on the document's job. Each names the value to move to, not a direction.

**No clickable links.** Byte-scan of the rendered PDF: `/Annots` 0, `/Link` 0, `/URI` 0. Seven call-to-action strings are dead text — the address, the GitHub and LinkedIn profiles, and four repository URLs. A recruiter clicking the GitHub URL is the highest-value action this document can produce, and it does nothing. Fix: `#link("mailto:…")[…]` in the header block, markdown links on repository URLs. Link text must stay identical to the visible string so extraction is byte-unchanged. Colour the four header links; leave body URLs slate so the header reads as the action zone.

**Headings are lighter than the body they head.** `#334155` at 10.34:1 against body `#0f172a` at 17.96:1. Entry headings should be `#0f172a`; section labels stay `#334155`, because caps, tracking and a rule already carry them and the contrast between a mid-slate label and a near-black entry title is the distinction wanted. Zero layout change, zero reflow — the cheapest high-yield change available.

**Emphasis exists only on the second page.** Bold runs inside body text: page 1 zero, page 2 seven. The page a skimmer is guaranteed to read is a flat grey field, and every quantified proof point sits on the page they may not reach. Fix is content-side but it is a hierarchy decision: bold four to six magnitude claims in page-1 bullets.

**The technical-expertise list has no hanging indent.** All six entries wrap, and continuation lines return flush with the bold term that opens the line, so the labels stop working as list markers and the section renders as an eleven-line wall at the top of page 2. Fix: `hanging-indent: 8pt` scoped to that section. Do not convert to a two-column table — column-order garbling in older ATS parsers is a real risk on a document whose ligatures were already disabled for extraction hardening.

**Page 1 has 2.36pt of slack.** Last ink at y=739.18 against a bottom margin at 741.54. One added word pushes a line, pushes a bullet to page 2, and lands the closing line on a third page. Any tailoring pass detonates it silently. Page 2 is 96.7% full, so it earns its existence and cannot absorb the overflow.

**Section rules will not survive printing.** `#e2e8f0` at 1.23:1 is roughly a 9% tint; a 0.6pt hairline at that tint frequently drops below the minimum reproducible dot on an office laser. When it goes, the section boundary rests on a 1pt size step. Fix: `#cbd5e1`, matching the header rule.

**The header does not read as a block.** Tagline→contacts 7.53pt, contacts→first section 9.57pt — and 9.57pt is exactly the gap every body paragraph gets, so the header receives no more separation than a paragraph does. The name rule compounds it: 14.5pt below the name and 21.2pt above the tagline, so it underlines the name rather than dividing header from content, and it puts two rules in the top 115pt. Fix: move the rule under the contact line.

**Inline mono is optically small.** Both faces carry an identical x-height (516/1000) — Plex Mono is designed to pair with Plex Sans at the same size — so the 9pt-against-10pt down-step compensates for a mismatch that does not exist. Fix: 9.5pt. 10pt matches exactly but Plex Mono's advance is ~20% wider and three of the four code runs sit on lines already at 103–110 characters.

**Eighteen wrap tails of twenty characters or fewer** — `matching.`, `needs it.`, `tooling.`, `(#5432).`, `2009.` and thirteen more. Caused by bullets overshooting a line boundary by two to four words, not by the measure. Fix by editing about eight bullets.

**Two theme rules are dead, and the theme's name size disagrees with the page by 7pt.** `#show link:` never fires (no links); `heading.where(level: 1)` never fires, because the header is a raw-typst block in the content — the theme specifies 19pt and the page renders 26pt. A future editor changing the theme's h1 will see nothing move. Fix: either move the header into the theme as a real level-1 heading, or delete the rule and point a comment at the content as the header's owner.

**`SUMMARY` is a zero-information label** occupying about 25pt of the most valuable space on the document, telling the reader that the first paragraph is the first paragraph. Deleting the heading and keeping the paragraph recovers that space — which is most of what the rhythm fixes above need.

**No document metadata.** No `/Title` or `/Author`, so browser tabs and preview windows show the filename. Fix: `#set document(title: …, author: …)`.

## Changing this document

1. Change `internal/pdfrender/resume.typ`, then update the tables here in the same commit. This file documents that file; if they disagree, the `.typ` is right and this is stale.
   The `file:line` anchors here go stale on any edit above them — three of them did so while this document was being written, from a comment change in `adapter.go`. Each is paired with the value or construct it points at, so a drifted number is recoverable by searching for that instead. Re-check them when you touch either file.
2. Re-render `testdata/resume-reference.md` and compare with `pdftotext -bbox`. Eyes do not catch a 4% weight change; the ratio does.
3. A **body size** change will red `TestEntrySubtitleRuleIsLive` — `controlWidthPt` is calibrated to the control run at 10pt. That is by design; re-measure the constant and say so in the commit. A **margin** change will not: the assertion is a ratio of glyph advances, not of layout.
4. Adding a `#show` rule reds `TestThemeRulesAreExercisedByReference` until it is either exercised by the reference or recorded with a reason. Choose deliberately; that prompt is the point.

## Deliberate deviations from the reference

| Deviation | Reference | Here | Standing |
|---|---|---|---|
| Bullet gap | 0cm | 0.78em | **Sound.** The reference's highlights are one-liners separated by leading alone. Ours wrap to two lines, and at 0cm a wrapped bullet's own lines sit as far apart as two separate bullets, destroying the grouping. |
| Section titles | 14pt bold sentence-case | 12pt bold all-caps | **Sound, and it survives on the page.** All-caps carries roughly the weight of a size step by itself; 14pt with caps on top would overshoot. Section labels do out-rank entry headings despite the 1pt gap, because caps, 0.7pt tracking and the rule together beat the size step. |
| Section gap | 0.5cm | 7.0mm | **Right in principle, undershot in practice.** The section boundary has to out-rank the entry boundary — but measured it is 9.57pt against 7.39pt, a 30% step where the reasoning wanted a clear one. See **Vertical rhythm**. |
| Entry headings | 10.5pt at 10pt body | 11pt | **Right conclusion, wrong lever.** A 5% step carries no size signal — but 10% does not either, and the document still reads flat there. The size axis was never the broken one: colour was (headings lighter than body) and weight is unavailable (only 400 and 700 ship). This deviation spent vertical space on the least effective of the three. |
| Name size | 30pt | 26pt | **Undocumented until now.** 26pt is defensible — 30pt on a dense two-page CV shouts — but the brief never owned the change, and the value lives in the content's raw-typst block rather than in the theme, so the theme's own h1 rule (19pt) describes nothing that renders. |
