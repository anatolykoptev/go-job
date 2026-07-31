# go-job Documents — Design System

**Surface**: the PDFs `internal/pdfrender` renders — `resume.pdf` and `cover.pdf`, written per application under `$UPLOADS_ROOT/go-job/applications/<job_id>/`.

**Not this surface**: the operator admin UI under `internal/adminui`. It serves these bytes and never styles them; do not read this file as governing it.

**Register**: document. A printed-or-screen-read artifact that must survive machine extraction, not an interface.

**Every measured number here comes from `internal/pdfrender/testdata/resume-reference.md` rendered through the theme in this repository.** Anyone can reproduce them, and nothing is quoted from a document the repository does not contain. Where a value is set rather than measured, its source is named.

## Audience

Two readers, and they want opposite things.

A **recruiter or hiring manager** skims: the top third, a decision, on to the next. Scanability and hierarchy beat density.

An **ATS parser** extracts text before any human sees it. It has no opinion about typography and every opinion about glyph encoding, and a layout choice that breaks extraction removes the document from consideration without anyone reading it.

Where the two conflict the parser wins, because it decides whether the human ever reads the page.

## Provenance

Scale and rhythm derive from **rendercv**'s `engineeringresumes` defaults — a project of comparable scale rendering resumes through the same Typst engine, so its numbers are a measured reference rather than a preference. Reference: body 10pt, name 30pt, section titles 1.4em bold, margins 0.7in on all four sides, line spacing 0.6em, 0.5cm above a section and 0.3cm below, 1.2em between entries, 0cm between bullet items.

Deviations are listed at the end with their reasons. Anything not listed is the reference, and changing it needs the same kind of evidence the reference has.

## Documents

Both take one path: markdown → `pandoc -t typst` → theme preamble prepended → `typst compile`, at `adapter.go:274`. Both request `Theme: "resume"` at `adapter.go:293`. go-kit ships `report`, `minimal`, `corporate`, `card` and `dark`; go-job registers its own `resume` over go-kit's built-in at `adapter.go:48`, and no other theme is in use.

`resume.pdf` is a raw-typst header, then `##` sections of `###`/`####` entries with bullets. `cover.pdf` uses the same theme for prose only — no headings, no entries — so most of the theme's rules do not fire on it, by design.

## Tokens

`internal/pdfrender/resume.typ` is authoritative; this is its documentation. If they disagree, the `.typ` is right and this is stale.

| Role | Size | Weight | Colour |
|---|---|---|---|
| Name | 26pt | bold | `#0f172a` |
| Section (`##`) | 12pt | bold, `tracking: 0.7pt`, uppercased | `#334155` |
| Entry (`###`) | 11pt | bold | `#0f172a` |
| Entry subtitle (`####`) | 10pt | regular | `#64748b` |
| Body | 10pt | regular | `#0f172a` |
| Inline / block code | 9.5pt | regular, IBM Plex Mono | `#1e293b` / `#334155` |
| Table header row | 9pt | bold | inherits |

The name is set by a raw typst block in the **content**, not by the theme. The theme's level-1 rule (19pt) never fires on a resume, because no `#` heading is ever emitted; it carries a comment saying so, since an editor changing 19pt would otherwise watch nothing move.

**An entry heading must be darker than the prose beneath it.** It was not — headings sat at `#334155` against body `#0f172a`, so the darkest mass on the page was running text and the titles a reader scans for receded behind their own bullets. Section labels stay `#334155` deliberately: caps, tracking and a rule already carry them, and a mid-slate label against a near-black entry title is the distinction wanted.

**Mono is 9.5pt, not 9pt.** Plex Sans and Plex Mono ship an identical x-height (516/1000) and are designed to pair at one size, so a 0.9× down-step corrected a mismatch that does not exist. 10pt would match exactly; 9.5pt reads level without Plex Mono's ~20% wider advance flipping a wrap.

**Rules are `#cbd5e1`, not `#e2e8f0`.** At 1.23:1 the old value is roughly a 9% tint, and a 0.6pt hairline at that tint frequently drops below the minimum reproducible dot on an office laser — the section boundary then rests on a 1pt size step. `#e2e8f0` survives on table row rules and the code-block border, where a lost hairline costs nothing.

Contrast on white, computed: `#0f172a` 17.85:1 · `#1e293b` 14.63:1 · `#334155` 10.35:1 · `#64748b` 4.76:1 (AA pass, AAA fail) · `#26428b` 9.40:1 · `#cbd5e1` 1.48:1 · `#e2e8f0` 1.23:1.

Links render `#26428b` wherever they appear, including inside an entry subtitle. Holding that colour to the header alone needs a rule scoped inside the level-4 block, and such a rule cannot reach a URL pandoc has wrapped onto its own line (**Known gaps**). One colour everywhere is the simpler and reliable choice.

### Page

US Letter, 612×792pt. Margins 17.8mm (0.7in) on all four sides, giving a **511.09pt** measure.

Wide by book typography: the fixture runs 18 of 63 body lines past 90 characters, median 58, longest 112. It is also the reference's own measure for this genre, and narrowing to ~90 characters needs ~1.2in margins and costs a page. Keep it — the actionable defect is short wrap tails, not the measure.

## Vertical rhythm

Every gap is set against the one beneath it, so each level reads as a level, and the gap below a heading stays smaller than the gap above it so a heading binds to its own content.

Measured ink-to-ink on the fixture:

| Boundary | Gap |
|---|---|
| Within a bullet | 2.98pt |
| Between bullets | 4.74–4.80pt |
| Subtitle → its first bullet | 6.09pt |
| Entry heading → its subtitle | 6.67pt |
| Entry → entry | 7.4pt |
| Section → section | 9.62pt |

Directive values and ink-to-ink measurements differ, because a text box starts at cap height and ends below the baseline. Verify with `pdftotext -bbox`, never by eye.

**Two weaknesses, recorded rather than fixed.** The subtitle has 6.67pt above and 6.09pt below, so it floats between its heading and its bullets instead of binding upward. And the entry boundary at 7.4pt is only ~10% larger than an intra-entry gap — invisible at reading distance, so an entry boundary is carried by its bold heading alone.

The knob for both is the `v()` closing the level-3 rule, **not** the level-4 block's `above:`. That parameter is fully dominated: swept −20mm to +6mm, the subtitle's `yMin` is identical to six decimals at every step. The theme records this where someone would otherwise reach for it.

## Authoring contract

**This is the part that has actually failed.** The theme is a set of `#show` rules, and a rule only reaches the page if the markdown carries the construct that triggers it. Between 2026-06-27 and 2026-07-31 the entry-subtitle rule fired zero times, because every entry had been written as a single `###` line. Nothing reported a fault: the PDF rendered, downloaded and opened.

`testdata/resume-reference.md` is a **theme gauge** — its job is to trigger every rule, not to model a resume, and it deliberately does not follow every rule below. Read the rules here; read the fixture for which constructs the theme styles.

- `##` opens a section. `###` is an entry title, the project or the job. `####` is the entry's one-line subtitle, the gloss of what the thing *is* — a distinct level, not a suffix on the `###` line. Collapsing the two is the failure above.
- **Keep a `####` subtitle short**, or pandoc wraps it and the tail leaves the heading (**Known gaps**).
- **No `## Summary` heading.** The opening paragraph is the summary by convention; a label costs about 25pt to say that the first paragraph is the first paragraph.
- **Every URL is a link** — `#link("mailto:…")[…]` in the header, markdown links on repository URLs. Link text must stay identical to the visible string, so extracted plaintext is byte-unchanged. A resume that argues from repositories and cannot be clicked through wastes its strongest affordance.
- **Emphasis carries the magnitudes, on every page.** Bold the quantified claims and nothing else. A page with no bold runs is a flat field a skimmer slides off; a page where everything is bold is the same field.
- Inline code uses backticks and renders in Plex Mono. Identifiers only, never emphasis.
- A table needs its delimiter row (`| --- |`); the header row above it is styled separately.
- A run-in definition list needs `#set par(hanging-indent: 8pt)` in a raw-typst block before it and `0pt` after — the bold term is the marker, so continuation lines must clear it. Scoped in content, because a hanging indent on ordinary prose reads as a mistake.
- Set `#set document(title:, author:)`, or browser tabs and preview windows show the filename.
- The name, tagline and contact line live in the content's raw-typst block. That is why they can drift, and why changing them is a design decision rather than a content edit.

## Hard constraints

### Only these weights exist

`fonts-ibm-plex` gives `IBM Plex Sans` weights **100/200/300/400/700**. Typst exposes 500 and 600 as *separate families* — `IBM Plex Sans Medm` and `IBM Plex Sans SmBld`.

Asking the plain family for `weight: "semibold"` resolves to **700 Bold** by nearest-weight, silently. Measured with a width ladder: 400 and 500 both 224.826pt, 600 and 700 both 233.046pt, an explicit `SmBld` 230.646pt.

Name only weights the family has. Real SemiBold means naming `"IBM Plex Sans SmBld"` *and* adding that family to `requiredFontFamilies`, or the preamble/required-list agreement test fails. Nothing detects a violation: `gojob_pdf_font_available` checks font **families**, never weights.

### Fonts must be in the image

The theme names IBM Plex Sans and IBM Plex Mono. Typst substitutes a missing family **without erroring** — for five weeks every resume rendered in Libertinus Serif while the renderer gauge read healthy. The Dockerfile's `fonts` stage copies the TTFs from an `ubuntu:24.04` stage; `gojob_pdf_font_available` and `GojobPdfFontMissing` exist because that failure has no other symptom.

### ATS

`ligatures: false` is non-negotiable — an `fi` ligature extracts as a private-use codepoint and garbles the word. The PDF is tagged, `/Lang` is set, and all 552 fixture words round-trip through `pdftotext`.

Prefer ASCII in prose. `≈` round-trips cleanly, so it is a voice issue rather than an extraction one, but it reads as engineer shorthand to a recruiter.

Links do not harm extraction: `/URI` annotations are ignored by text extractors, and identical link text leaves the plaintext byte-unchanged.

## What is gated, and what is not

`reference_test.go` holds two halves of the theme contract: every `#show` rule is either exercised by the fixture or recorded as knowingly unexercised with a reason; and the entry-subtitle rule is measured on a real render, as a ratio against a control run so the assertion survives a toolchain change.

**Neither gates the markdown that ships.** The document `PDF()` renders is supplied by its caller and never read from the fixture, so a resume authored flat still renders with the level-4 rule firing zero times and both tests green. Tracked in #409.

CI pins typst 0.14.2 and fonts-ibm-plex 6.1.1-1; the runtime image pins neither. Tracked in #410.

## Known gaps

- **A long `####` subtitle is amputated.** The adapter passes no `--wrap`, so pandoc breaks its typst output at 72 columns, and a typst heading ends at end of line — the tail becomes a body paragraph in body colour. Affects every document this service renders. Tracked in #415.
- **Coverage is keyed by selector, not by rule.** Two rules naming the same selector collapse into one slot and the count guard still balances, so deleting either would red nothing. Tracked in #416.
- **Wrap tails.** Nine short lines on the fixture, caused by content overshooting a line boundary by a few words rather than by any value here.
- **The theme's level-1 rule describes nothing that renders.** The header is content, at a size the theme does not control. Closing this means moving the header into the theme, which changes the content contract for every future document.

## Changing this document

1. Change `resume.typ`, then update the tables here in the same commit.
2. Re-render `testdata/resume-reference.md` and compare with `pdftotext -bbox`. Eyes do not catch a 4% weight change; a ratio does.
3. A **body size** change reds `TestEntrySubtitleRuleIsLive` — `controlWidthPt` is calibrated at 10pt. By design: re-measure the constant and say so in the commit. A **margin** change does not; the assertion is a ratio of glyph advances, not of layout.
4. Adding a `#show` rule reds `TestThemeRulesAreExercisedByReference` until it is exercised by the fixture or recorded with a reason. That prompt is the point.
5. The `adapter.go` line anchors here drift on any edit above them — three did so twice while this file was being written. Each is paired with the construct it names, so search for that rather than trusting the number.

## Deviations from the reference

| Deviation | Reference | Here | Standing |
|---|---|---|---|
| Bullet gap | 0cm | 0.78em | **Sound.** The reference's highlights are one-liners separated by leading alone; ours wrap, and at 0cm a wrapped bullet's own lines sit as far apart as two separate bullets. |
| Section titles | 14pt bold sentence-case | 12pt bold all-caps | **Sound, and it holds on the page.** All-caps carries roughly a size step by itself; section labels do out-rank entry headings despite the 1pt gap, because caps, tracking and a rule together beat it. |
| Section gap | 0.5cm | `v(4.5mm)` | **Undershoot.** The section boundary must out-rank the entry boundary; measured it is 9.62pt against 7.4pt, a 30% step where the reasoning wanted a clear one. |
| Entry gap | 1.2em (4.23mm) | `v(3.7mm)` | **Undershoot, and unowned until now.** This is also the knob that governs the subtitle's binding. |
| Entry headings | 10.5pt at 10pt body | 11pt | **Right conclusion, wrong lever.** A 5% step carries no size signal — but neither does 10%. The size axis was never the broken one: colour was, and weight is unavailable because only 400 and 700 ship. |
| Name size | 30pt | 26pt | **Defensible, previously unowned.** 30pt on a dense two-page CV shouts. The value lives in content, so the theme's own 19pt level-1 rule describes nothing that renders. |
