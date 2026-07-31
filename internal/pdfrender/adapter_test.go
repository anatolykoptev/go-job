package pdfrender

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/anatolykoptev/go-kit/render/typst"
)

// TestNormalizeTitleBlock covers the pure normalizeTitleBlock helper.
// No external binaries required.
func TestNormalizeTitleBlock(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "one-line title block",
			in:   "% Anatoly Koptev — AI Infrastructure Engineer\n\nBody paragraph.\n",
			want: "# Anatoly Koptev — AI Infrastructure Engineer\n\nBody paragraph.\n",
		},
		{
			name: "two-line block: title + contact",
			in:   "% Anatoly Koptev — AI Infrastructure Engineer\n% San Francisco Bay Area · me@example.com\n\nBody paragraph.\n",
			want: "# Anatoly Koptev — AI Infrastructure Engineer\n\nSan Francisco Bay Area · me@example.com\n\nBody paragraph.\n",
		},
		{
			name: "three-line block: title + author + date",
			in:   "% My Resume\n% Jane Doe\n% January 2025\n\nIntroduction.\n",
			want: "# My Resume\n\nJane Doe\nJanuary 2025\n\nIntroduction.\n",
		},
		{
			name: "plain markdown with no title block — unchanged",
			in:   "# Already a Heading\n\nSome text.\n",
			want: "# Already a Heading\n\nSome text.\n",
		},
		{
			name: "plain markdown paragraph first — unchanged",
			in:   "First paragraph, no title block.\n\n## Section\n",
			want: "First paragraph, no title block.\n\n## Section\n",
		},
		{
			name: "percent appearing only mid-document — unchanged",
			in:   "## Introduction\n\nSee 50% improvement.\n",
			want: "## Introduction\n\nSee 50% improvement.\n",
		},
		{
			name: "percent mid-line in opening paragraph — unchanged",
			in:   "This is 100% correct.\n",
			want: "This is 100% correct.\n",
		},
		{
			name: "leading blank lines before the title block",
			in:   "\n\n% Late Title\n\nBody.\n",
			want: "\n\n# Late Title\n\nBody.\n",
		},
		{
			name: "title block is the only content (single line)",
			in:   "% Solo Title\n",
			want: "# Solo Title\n",
		},
		{
			name: "title block only content, two lines",
			in:   "% Title\n% Author\n",
			want: "# Title\n\nAuthor\n",
		},
		{
			name: "CRLF line endings preserved",
			in:   "% Title CRLF\r\n% Author CRLF\r\n\r\nBody.\r\n",
			want: "# Title CRLF\r\n\r\nAuthor CRLF\r\n\r\nBody.\r\n",
		},
		{
			name: "bare % as first title line (empty title field)",
			in:   "%\n% Author Only\n\nBody.\n",
			want: "# \n\nAuthor Only\n\nBody.\n",
		},
		{
			name: "title block immediately followed by content (no blank line)",
			in:   "% Title\nImmediate body.\n",
			want: "# Title\n\nImmediate body.\n",
		},
		{
			name: "fourth % line is NOT part of title block (at most 3)",
			// Only 3 title lines consumed; "% Fourth line" becomes part of rest.
			in:   "% Title\n% Author\n% Date\n% Fourth line\n\nBody.\n",
			want: "# Title\n\nAuthor\nDate\n\n% Fourth line\n\nBody.\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := normalizeTitleBlock(tc.in)
			if got != tc.want {
				t.Errorf("normalizeTitleBlock mismatch\n--- input ---\n%q\n--- want ---\n%q\n--- got  ---\n%q",
					tc.in, tc.want, got)
			}
		})
	}

	// Falsification: confirm the function does NOT return input unchanged when
	// a title block is present. If normalizeTitleBlock were a no-op this would
	// pass — but only because the "%" would still be there, which is the bug.
	// Reverting normalizeTitleBlock to `return md` makes this subtest RED.
	t.Run("falsification: title block is converted (not passed through)", func(t *testing.T) {
		t.Parallel()
		in := "% Must Become Heading\n\nBody.\n"
		got := normalizeTitleBlock(in)
		if strings.HasPrefix(got, "%") {
			t.Errorf("normalizeTitleBlock left a leading %%: title block was NOT converted; got %q", got)
		}
		if !strings.HasPrefix(got, "# ") {
			t.Errorf("normalizeTitleBlock did not produce a heading; got %q", got)
		}
	})
}

// TestNormalizeTitleBlock_NoOp confirms that the common path (no title block)
// returns a byte-identical string, preserving existing behaviour.
func TestNormalizeTitleBlock_NoOp(t *testing.T) {
	t.Parallel()
	noTitleCases := []string{
		"",
		"\n",
		"# Heading\n\nText.\n",
		"Regular paragraph without percent.\n",
		"50% of the time it works every time.\n",
	}
	for _, in := range noTitleCases {
		got := normalizeTitleBlock(in)
		if got != in {
			t.Errorf("normalizeTitleBlock modified a non-title-block input:\n--- input ---\n%q\n--- got  ---\n%q", in, got)
		}
	}
}

// TestPDF_TitleBlockRendered is an end-to-end smoke test that renders a
// markdown document with a pandoc title block and verifies the PDF text
// contains the title without a leading "%" character. Skipped when typst or
// pandoc is absent. Also requires pdftotext (poppler-utils) for verification.
func TestPDF_TitleBlockRendered(t *testing.T) {
	for _, bin := range []string{"typst", "pandoc", "pdftotext"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH — skipping end-to-end PDF test", bin)
		}
	}

	a := New()
	if !a.Ready() {
		t.Skip("TypstAdapter.Ready() returned false — skipping end-to-end PDF test")
	}

	md := "% Anatoly Koptev — AI Infrastructure Engineer / Architect\n% San Francisco Bay Area · me@example.com\n\n## Summary\n\nExperienced engineer.\n"
	pdfBytes, err := a.PDF(context.Background(), md)
	if err != nil {
		t.Fatalf("PDF() error: %v", err)
	}
	if len(pdfBytes) == 0 {
		t.Fatal("PDF() returned empty bytes")
	}

	tmp := t.TempDir()
	pdfPath := tmp + "/out.pdf"
	txtPath := tmp + "/out.txt"

	if err := os.WriteFile(pdfPath, pdfBytes, 0o600); err != nil {
		t.Fatalf("write pdf: %v", err)
	}

	out, err := exec.Command("pdftotext", pdfPath, txtPath).CombinedOutput()
	if err != nil {
		t.Fatalf("pdftotext: %v\n%s", err, out)
	}

	txtBytes, err := os.ReadFile(txtPath)
	if err != nil {
		t.Fatalf("read txt: %v", err)
	}
	text := string(txtBytes)

	// Title must appear as "Anatoly Koptev — …", NOT "% Anatoly Koptev — …".
	if strings.Contains(text, "% Anatoly") {
		t.Errorf("PDF text still contains literal %%: first lines: %q", firstLines(text, 5))
	}
	if !strings.Contains(text, "Anatoly Koptev") {
		t.Errorf("PDF text does not contain expected title; first lines: %q", firstLines(text, 5))
	}
}

// firstLines returns the first n non-empty lines of s, joined by " | ".
func firstLines(s string, n int) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
			if len(out) >= n {
				break
			}
		}
	}
	return strings.Join(out, " | ")
}

// TestThemeRegistration verifies that go-job's init() registered the approved
// resume theme under the name "resume", replacing go-kit's built-in of the
// same name. This is the silent-failure surface: if the registration never
// runs, the built-in renders instead and nothing reports a fault — the output
// is still a plausible resume, just in the wrong layout.
//
// typst.LookupTheme is the exported API that resolveTypstTheme calls
// internally during a real render (a.PDF → r.Render → pdfSource →
// resolveTypstTheme). So this test checks exactly what the adapter would
// resolve when it passes Theme: "resume".
//
// Anchors on values that DIFFER between the two themes:
//   - go-job template: 17.8mm margins, level-4 #show rule
//   - go-kit built-in: 16mm margins, no level-4 rule
//
// Asserts both directions: ours present, theirs absent. A test that only
// checks "some resume theme resolved" passes with the bug in place.
//
// Falsification: comment out the RegisterTheme call in init() → the built-in
// is returned instead → the 17.8mm assertion fails AND the 16mm assertion
// fails (built-in has 16mm, which we assert absent).
func TestThemeRegistration(t *testing.T) {
	t.Parallel()

	theme, ok := typst.LookupTheme("resume")
	if !ok {
		t.Fatal("typst.LookupTheme(\"resume\") returned ok=false — no theme registered under \"resume\"")
	}

	preamble := theme.Preamble

	// Ours present: 17.8mm margins (go-job template).
	if !strings.Contains(preamble, "17.8mm") {
		t.Errorf("resume theme preamble does NOT contain 17.8mm — go-job template is not registered; got built-in or wrong theme.\npreamble first 200 chars: %q", preamble[:min(200, len(preamble))])
	}

	// Ours present: level-4 #show rule (go-job template has it, built-in does not).
	if !strings.Contains(preamble, "level: 4") {
		t.Errorf("resume theme preamble does NOT contain 'level: 4' — go-job template's level-4 show rule is missing; got built-in or wrong theme.\npreamble first 200 chars: %q", preamble[:min(200, len(preamble))])
	}

	// Theirs absent: 16mm margins (go-kit built-in has these, ours does not).
	if strings.Contains(preamble, "16mm") {
		t.Errorf("resume theme preamble contains 16mm — go-kit built-in is registered instead of go-job template.\npreamble first 200 chars: %q", preamble[:min(200, len(preamble))])
	}
}
