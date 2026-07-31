// Package pdfrender provides the TypstAdapter that implements
// applications.Renderer using go-kit/render/typst + go-kit/fileopt.
//
// This package is the ONLY place in go-job that imports render/typst or
// fileopt — keeping os/exec out of the domain packages under engine/jobs.
// Injected at main.go composition root; never imported by engine/jobs/applications.
package pdfrender

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/anatolykoptev/go-kit/fileopt"
	"github.com/anatolykoptev/go-kit/render"
	"github.com/anatolykoptev/go-kit/render/typst"
	"github.com/anatolykoptev/go_job/internal/engine/jobs/applications"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// resumeTypstPreamble is the approved resume theme preamble, measured over
// eleven iterations against the host's IBM Plex build. Embedded verbatim from
// the approved design source — every number (17.8mm margins, 10pt size, 0.6em
// leading, 0.78em spacing, the level-2/3/4 v() values, the level-4 #show rule)
// was set by rendering a PDF and adjusting; retyping or rounding any value
// silently discards that work.
//
//go:embed resume.typ
var resumeTypstPreamble string

// init registers go-job's resume theme under the name "resume", replacing
// go-kit's built-in of the same name. This is the supported mechanism for a
// product to own its look without editing the shared theme set.
//
// The hazard: if this registration never runs, or runs after the first render,
// the built-in renders instead and nothing reports a fault — the output is
// still a plausible resume, just in the wrong layout. The built-in has 16mm
// margins and no level-4 show rule; ours has 17.8mm margins and a level-4
// show rule. TestThemeRegistration anchors on both directions.
func init() {
	typst.RegisterTheme(typst.Theme{
		Name:         "resume",
		Preamble:     resumeTypstPreamble,
		PageMarginPt: 24,
	})
}

// pdfRendererAvailableGauge is set to 1 at startup when both typst and pandoc
// are on PATH, 0 otherwise. Alerts on this gauge catch silently-degraded
// deployments where application_persist falls back to md-only.
var pdfRendererAvailableGauge = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "gojob_pdf_renderer_available",
	Help: "1 if typst and pandoc binaries are on PATH; 0 if absent (application_persist degrades to md-only).",
})

// pdfFontAvailableGauge is the detector for the class this package's font
// handling exists to close. Binary presence and FACE presence are different
// facts: typst substitutes a missing family without erroring, so an image with
// typst but without IBM Plex renders every resume in a fallback serif while
// gojob_pdf_renderer_available reads 1 and every request "succeeds".
//
// That was the state of the running container on 2026-07-31 — typst 0.14.2,
// pandoc 3.10, and four font families, none of them IBM Plex. Nothing reported
// it. Shipping the fonts fixes the instance; this gauge is what makes a
// recurrence visible, because a base-image bump or a renamed apt package
// reopens it just as silently.
var pdfFontAvailableGauge = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "gojob_pdf_font_available",
	Help: "1 if every font family the resume theme names is visible to typst; 0 if any is absent (renders silently substitute another face).",
})

// requiredFontFamilies are the families resume.typ names in its #set text and
// #show raw rules. It IS a hand-kept list — what is derived is the agreement
// between it and the preamble, which TestRequiredFontFamiliesMatchPreamble
// checks in both directions. A face the preamble starts naming and this list
// does not know about would go missing silently, which is the whole point.
var requiredFontFamilies = []string{"IBM Plex Sans", "IBM Plex Mono"}

// fontProbeTimeout bounds the startup font probe. `typst fonts` is a local
// filesystem scan measured at ~40ms in the deployed image; the ceiling exists
// for a pathological mount, not for the normal case.
const fontProbeTimeout = 15 * time.Second

// ligaPreamble is a pandoc raw-typst block that disables OpenType ligature
// substitution for the entire document body. Prepended to every markdown
// payload before rendering.
//
// Mechanism: pandoc passes ```` ```{=typst} ```` fenced blocks through to its
// typst output unchanged (verified pandoc 3.1.3 / typst 0.14.2). The resulting
// `#set text(features: (liga: 0))` directive appears after the theme preamble's
// `#set text(font: …)` call and scopes to all subsequent body content, composing
// with (not replacing) the theme's font settings.
//
// Why needed: IBM Plex Sans may substitute "fi"/"fl"/"ffi" etc. with a ligature
// glyph. Even when Typst writes a /ToUnicode CMap, some font builds map the
// ligature glyph to a private-use codepoint rather than the decomposed ASCII
// sequence. Setting liga: 0 prevents the substitution entirely — the glyph never
// appears, pdftotext extracts clean ASCII. Guards F1 (render_test.go).
const ligaPreamble = "```{=typst}\n#set text(features: (liga: 0))\n```\n\n"

// TypstAdapter implements applications.Renderer via the Typst backend.
// Zero chromedp/cdproto — pandoc + typst static binaries only.
type TypstAdapter struct {
	r *typst.TypstRenderer

	// fontLister enumerates the families typst can see. Injectable because the
	// DECISION built on it — which gauge value, which log — is the part that
	// breaks silently, and a test that can only reach the parsing leaves it
	// unguarded. Review proved that: with the whole probe block deleted from
	// Ready, the package stayed green.
	fontLister func(context.Context) ([]byte, error)
}

// New creates a TypstAdapter. No state is held by TypstRenderer; safe for
// concurrent use.
func New() *TypstAdapter {
	return &TypstAdapter{r: typst.NewTypstRenderer(), fontLister: typstFontList}
}

// Ready probes typst and pandoc availability, sets both PDF gauges, and returns
// true when both binaries are on PATH. Call once at startup after constructing
// the adapter.
//
// Font absence deliberately does NOT make this return false, because a PDF in
// the wrong face is still a usable PDF and the operator needs it reported, not
// withheld. gojob_pdf_font_available plus an Error log is that report.
//
// (This return value is a weak lever anyway: its one production consumer,
// main.go, only emits a Warn. The per-render md-only degrade is decided
// elsewhere, by isNoBinary on ErrNoBinary.)
func (a *TypstAdapter) Ready() bool {
	// LookPath on the SAME binary the font probe and the renderer use, so the
	// two gauges cannot disagree: with RENDER_TYPST_PATH set to an out-of-PATH
	// install, a bare LookPath("typst") would report the renderer absent while
	// the font probe read that install's fonts and reported them present.
	_, typstErr := exec.LookPath(typstBinary())
	_, pandocErr := exec.LookPath("pandoc")
	available := typstErr == nil && pandocErr == nil
	if available {
		pdfRendererAvailableGauge.Set(1)
	} else {
		pdfRendererAvailableGauge.Set(0)
	}
	a.checkFonts(context.Background(), typstErr == nil)
	return available
}

// checkFonts sets pdfFontAvailableGauge from a live enumeration and reports what
// is missing. Called unconditionally: an unconfirmed font set is unconfirmed
// whether typst is absent or merely unreadable, and both are 0.
//
// typstPresent splits only the LOG LEVEL, not the gauge. WITH_PDF=0 is the
// Dockerfile default and a supported build; an Error on every boot of an
// intended configuration devalues the Error that means something, which is the
// alert this gauge exists to feed. gojob_pdf_renderer_available already carries
// which of the two cases it is.
func (a *TypstAdapter) checkFonts(ctx context.Context, typstPresent bool) {
	out, err := a.fontLister(ctx)
	if err != nil {
		pdfFontAvailableGauge.Set(0)
		if typstPresent {
			slog.Error("pdfrender: typst is present but its font list could not be read — the resume theme's faces are unconfirmed", "err", err)
		} else {
			slog.Warn("pdfrender: typst absent, resume font set unconfirmed — expected on a WITH_PDF=0 build", "err", err)
		}
		return
	}
	if missing := missingFontFamilies(out, requiredFontFamilies); len(missing) > 0 {
		pdfFontAvailableGauge.Set(0)
		slog.Error("pdfrender: font families named by the resume theme are absent from this image — typst substitutes silently, so every resume renders in the wrong face",
			"missing", missing)
		return
	}
	pdfFontAvailableGauge.Set(1)
}

// typstFontList shells out to `typst fonts`, which prints one family name per
// line. Split from the parsing so the parse is testable without a binary.
//
// Binary resolution mirrors go-kit's resolveEnvOrPath precedence
// (RENDER_TYPST_PATH, then the legacy VAELOR_TYPST_PATH, then PATH). Probing a
// different installation than the renderer uses would measure the wrong font
// set and report it as fact.
func typstFontList(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, fontProbeTimeout)
	defer cancel()
	//nolint:gosec // bin resolved from the same env keys go-kit honours, or PATH
	return exec.CommandContext(ctx, typstBinary(), "fonts").Output()
}

// typstBinary resolves the typst executable the renderer would use.
func typstBinary() string {
	for _, key := range []string{"RENDER_TYPST_PATH", "VAELOR_TYPST_PATH"} {
		if p := os.Getenv(key); p != "" {
			return p
		}
	}
	return "typst"
}

// missingFontFamilies returns the required families absent from `typst fonts`
// output, in the order given.
//
// Exact line match, not substring: typst lists weight variants as separate
// families ("IBM Plex Sans SmBld", "IBM Plex Sans Thai"), and a substring test
// would accept an image carrying only those while the plain family the preamble
// asks for is missing.
func missingFontFamilies(fontList []byte, required []string) []string {
	visible := make(map[string]bool)
	for _, line := range strings.Split(string(fontList), "\n") {
		if f := strings.TrimSpace(line); f != "" {
			visible[f] = true
		}
	}
	var missing []string
	for _, family := range required {
		if !visible[family] {
			missing = append(missing, family)
		}
	}
	return missing
}

// PDF converts markdown to a PDF byte slice using pandoc + typst + fileopt.
//
// The liga-suppression preamble is prepended to the markdown before rendering
// to ensure clean ATS text extraction (F1 invariant).
//
// When pandoc or typst binaries are absent, PDF returns applications.ErrNoBinary
// (wrapped) so the caller can use errors.Is(err, applications.ErrNoBinary) to
// distinguish "binary absent" (soft skip) from real render failures.
func (a *TypstAdapter) PDF(ctx context.Context, md string) ([]byte, error) {
	// Normalize any pandoc title block before prepending ligaPreamble.
	// Pandoc only recognises lines starting with "% " as a title block when
	// they appear at the very top of the document. Prepending ligaPreamble
	// displaces them, so pandoc passes "%" through literally. Converting to
	// standard markdown (heading + paragraph) makes them position-independent.
	mdNorm := normalizeTitleBlock(md)

	// Inject ATS-safe liga suppression as a pandoc raw-typst block.
	mdWithPreamble := ligaPreamble + mdNorm

	raw, err := a.r.Render(ctx, mdWithPreamble, "markdown", render.Options{
		// "resume" theme: compact single-column US-Letter layout (IBM Plex Sans,
		// left-aligned, 20mm margins, no footer date) tuned to keep a content-rich
		// resume on one page. The liga suppression above composes with its #set text().
		Theme: "resume",
	})
	if err != nil {
		// render/typst emits "pandoc binary not found ..." or
		// "typst binary not found ..." — wrap as ErrNoBinary at this adapter
		// boundary so applications.isNoBinary uses errors.Is (not substr scan).
		if isNoBinaryErr(err) {
			return nil, fmt.Errorf("pdfrender: %w", applications.ErrNoBinary)
		}
		return nil, fmt.Errorf("pdfrender: typst render: %w", err)
	}

	// Optimize via gs+qpdf (text-only PDFs: gs is skipped, qpdf linearizes).
	// Missing binaries: fileopt degrades gracefully (returns original bytes + warn).
	out, err := fileopt.OptimizePDF(ctx, raw, fileopt.LevelEbook)
	if err != nil {
		slog.Warn("pdfrender: PDF optimize failed, serving raw bytes", "err", err)
		return raw, nil
	}
	return out, nil
}

// normalizeTitleBlock converts a pandoc title block at the top of md into
// standard markdown so that it renders correctly regardless of what is
// prepended before the document.
//
// A pandoc title block consists of up to 3 consecutive lines at the start of
// the document (after any leading blank lines) that each begin with "% " or
// are exactly "%" (empty field). Pandoc only recognises this block when those
// lines are the very first non-blank content it sees. Because ligaPreamble is
// prepended to md before rendering, the title block is displaced and pandoc
// passes the literal "%" characters through to the PDF unchanged.
//
// Conversion rules:
//   - The first "% …" line becomes a level-1 heading: "# <content>".
//   - Subsequent "% …" lines (author, date) become paragraph lines, separated
//     from the heading by a single blank line.
//   - Leading blank lines before the title block are preserved.
//   - A blank line between the converted block and the rest of the document is
//     inserted when absent.
//   - If no title block is present at the top, md is returned unchanged
//     (byte-identical — this is the common path).
//   - "%" characters that appear mid-document or mid-line are left untouched;
//     only the leading consecutive block is converted.
//   - CRLF line endings are detected and preserved.
func normalizeTitleBlock(md string) string {
	// Normalize line endings for processing; restore CRLF at the end.
	crlf := strings.Contains(md, "\r\n")
	s := strings.ReplaceAll(md, "\r\n", "\n")
	lines := strings.Split(s, "\n")

	// Skip leading blank lines.
	leadingBlanks := 0
	for leadingBlanks < len(lines) && lines[leadingBlanks] == "" {
		leadingBlanks++
	}

	// Collect consecutive title-block lines (at most 3).
	const maxTitleLines = 3
	end := leadingBlanks
	for end < len(lines) && end-leadingBlanks < maxTitleLines {
		line := lines[end]
		if line == "%" || strings.HasPrefix(line, "% ") {
			end++
		} else {
			break
		}
	}

	titleCount := end - leadingBlanks
	if titleCount == 0 {
		return md // no title block — byte-identical
	}

	titleLines := lines[leadingBlanks:end]
	restLines := lines[end:]

	var sb strings.Builder

	// Re-emit leading blank lines.
	for k := 0; k < leadingBlanks; k++ {
		sb.WriteByte('\n')
	}

	// First title line → level-1 heading.
	first := titleLines[0]
	var firstContent string
	if strings.HasPrefix(first, "% ") {
		firstContent = first[2:]
	}
	sb.WriteString("# ")
	sb.WriteString(firstContent)
	sb.WriteByte('\n')

	// Subsequent title lines (author, date) → paragraph, preceded by a blank line.
	if len(titleLines) > 1 {
		sb.WriteByte('\n')
		for _, tl := range titleLines[1:] {
			var content string
			if strings.HasPrefix(tl, "% ") {
				content = tl[2:]
			}
			sb.WriteString(content)
			sb.WriteByte('\n')
		}
	}

	// Rest of the document, with a blank separator when the next line is non-empty.
	if len(restLines) > 0 {
		if restLines[0] != "" {
			sb.WriteByte('\n')
		}
		sb.WriteString(strings.Join(restLines, "\n"))
	}

	result := sb.String()
	if crlf {
		result = strings.ReplaceAll(result, "\n", "\r\n")
	}
	return result
}

// isNoBinaryErr reports whether err from render/typst indicates a missing
// binary (typst or pandoc). go-kit wraps typst.ErrBinaryNotFound at both
// LookPath-miss sites, so errors.Is matches regardless of how the error text
// is worded — the sentinel survives rewording, the substring does not.
func isNoBinaryErr(err error) bool {
	return errors.Is(err, typst.ErrBinaryNotFound)
}
