// Package pdfrender provides the TypstAdapter that implements
// applications.Renderer using go-kit/render/typst + go-kit/fileopt.
//
// This package is the ONLY place in go-job that imports render/typst or
// fileopt — keeping os/exec out of the domain packages under engine/jobs.
// Injected at main.go composition root; never imported by engine/jobs/applications.
package pdfrender

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/anatolykoptev/go-kit/fileopt"
	"github.com/anatolykoptev/go-kit/render"
	"github.com/anatolykoptev/go-kit/render/typst"
	"github.com/anatolykoptev/go_job/internal/engine/jobs/applications"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// pdfRendererAvailableGauge is set to 1 at startup when both typst and pandoc
// are on PATH, 0 otherwise. Alerts on this gauge catch silently-degraded
// deployments where application_persist falls back to md-only.
var pdfRendererAvailableGauge = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "gojob_pdf_renderer_available",
	Help: "1 if typst and pandoc binaries are on PATH; 0 if absent (application_persist degrades to md-only).",
})

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
}

// New creates a TypstAdapter. No state is held by TypstRenderer; safe for
// concurrent use.
func New() *TypstAdapter {
	return &TypstAdapter{r: typst.NewTypstRenderer()}
}

// Ready probes typst and pandoc availability, sets the
// gojob_pdf_renderer_available gauge (1=present, 0=absent), and returns true
// when both are on PATH. Call once at startup after constructing the adapter.
func (a *TypstAdapter) Ready() bool {
	_, typstErr := exec.LookPath("typst")
	_, pandocErr := exec.LookPath("pandoc")
	available := typstErr == nil && pandocErr == nil
	if available {
		pdfRendererAvailableGauge.Set(1)
	} else {
		pdfRendererAvailableGauge.Set(0)
	}
	return available
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
		// "report" theme: professional A4 layout with IBM Plex Sans.
		// The liga suppression above composes with the preamble's #set text().
		Theme: "report",
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
// binary. render/typst produces exactly:
//
//	"pandoc binary not found (set RENDER_PANDOC_PATH or ensure pandoc is on PATH)"
//	"typst binary not found (set RENDER_TYPST_PATH or ensure typst is on PATH)"
//
// The substring "binary not found" is specific to these two cases — it does NOT
// appear in typst compile errors (e.g. "typst compile: ...\nstderr: file not
// found" for a missing image uses "file not found", not "binary not found").
func isNoBinaryErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "binary not found")
}
