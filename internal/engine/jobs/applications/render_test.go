package applications_test

// render_test.go — F1 ATS ligature golden test.
//
// Fitness function F1: render a fixture resume via the Typst pipeline and
// assert that pdftotext extracts the ASCII ligature words without ligature
// codepoints (U+FB00–FB06). This guards the load-bearing ATS extraction
// invariant from R1: "Typst emits a ligature glyph WITHOUT a clean ToUnicode
// map → ATS can't extract 'Staﬀ'".
//
// Skips cleanly when typst, pandoc, or pdftotext are absent — never blocks the
// slim public build. Runs in PDF-enabled CI (WITH_PDF=true).
//
// RED-on-revert: remove pdfrender's liga-suppression injection (or remove the
// `#set text(features: (liga: 0))` raw block from the fixture md) and this test
// fails on fonts that substitute a ligature glyph whose ToUnicode entry maps to
// the private-use block instead of the constituent codepoints.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/anatolykoptev/go_job/internal/pdfrender"
)

// ligatureFixtureMD is a minimal resume snippet that triggers ff/fi/fl/ffi/ffl
// ligature substitution in typical sans-serif fonts.
const ligatureFixtureMD = `# John Staff

## Experience

**Staff Engineer** — Efficiency Corp (2022–present)

Led workflow automation initiatives across affiliate partners.
Delivered a profitable self-sufficiency programme in three offices.

## Skills

- Efficient code review practices
- Workflow orchestration
- Staff development and affiliation
`

// TestF1_ATSLigatureExtraction renders the fixture via the Typst adapter and
// asserts pdftotext output is free of ligature codepoints U+FB00–FB06.
func TestF1_ATSLigatureExtraction(t *testing.T) {
	// Skip when required binaries are absent.
	for _, bin := range []string{"typst", "pandoc", "pdftotext"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("skipping F1: %s not on PATH (install typst+pandoc+pdftotext for PDF-enabled CI)", bin)
		}
	}

	adapter := pdfrender.New()
	pdf, err := adapter.PDF(context.Background(), ligatureFixtureMD)
	if err != nil {
		t.Fatalf("adapter.PDF: %v", err)
	}
	if len(pdf) == 0 {
		t.Fatal("adapter.PDF returned empty bytes")
	}

	// Extract text via pdftotext (poppler).
	extracted := extractTextFromPDF(t, pdf)

	// Assert the fixture words are present in ASCII form.
	for _, word := range []string{"Staff", "Efficiency", "workflow", "affiliate"} {
		if !strings.Contains(extracted, word) {
			t.Errorf("pdftotext output missing expected word %q (ligature corruption?)", word)
		}
	}

	// Assert no ligature codepoints U+FB00–FB06.
	var ligatureRunes []rune
	for _, r := range extracted {
		if r >= 0xFB00 && r <= 0xFB06 {
			ligatureRunes = append(ligatureRunes, r)
		}
	}
	if len(ligatureRunes) != 0 {
		t.Errorf("pdftotext extracted %d ligature codepoint(s): %q — "+
			"add #set text(features: (liga: 0)) to the Typst preamble",
			len(ligatureRunes), string(ligatureRunes))
		for _, r := range ligatureRunes {
			t.Logf("  U+%04X %s", r, fmt.Sprintf("%c", r))
		}
	}
}

// extractTextFromPDF writes pdf to a temp file, runs pdftotext, and returns the output.
func extractTextFromPDF(t *testing.T, pdf []byte) string {
	t.Helper()
	dir := t.TempDir()
	pdfPath := dir + "/test.pdf"
	if err := os.WriteFile(pdfPath, pdf, 0o600); err != nil {
		t.Fatalf("write temp pdf: %v", err)
	}
	var out bytes.Buffer
	ctx := context.Background()
	cmd := exec.CommandContext(ctx, "pdftotext", pdfPath, "-") //nolint:gosec // test only, controlled paths
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("pdftotext: %v", err)
	}
	return out.String()
}
