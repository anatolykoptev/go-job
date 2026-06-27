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

	"github.com/anatolykoptev/go-kit/fileopt"
	"github.com/anatolykoptev/go-kit/render"
	"github.com/anatolykoptev/go-kit/render/typst"
)

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

// PDF converts markdown to a PDF byte slice using pandoc + typst + fileopt.
//
// The liga-suppression preamble is prepended to the markdown before rendering
// to ensure clean ATS text extraction (F1 invariant).
//
// Graceful degrade: when pandoc or typst binaries are absent, the error from
// the renderer contains "not found" / "binary not found" — the authority
// (applications.Persist) classifies this and soft-skips the PDF step.
func (a *TypstAdapter) PDF(ctx context.Context, md string) ([]byte, error) {
	// Inject ATS-safe liga suppression as a pandoc raw-typst block.
	mdWithPreamble := ligaPreamble + md

	raw, err := a.r.Render(ctx, mdWithPreamble, "markdown", render.Options{
		// "report" theme: professional A4 layout with IBM Plex Sans.
		// The liga suppression above composes with the preamble's #set text().
		Theme: "report",
	})
	if err != nil {
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
