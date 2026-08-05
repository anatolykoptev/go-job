package jobs

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/anatolykoptev/go_job/internal/pdfrender"
)

// resumeHeaderShape is the operator-approved typst header block: the vertical
// rhythm (#v values), the colours, and the divider placement (under the
// contacts, never under the name) are load-bearing — matched to a reference
// to three decimals. The tests below pin each of those, and each maps to a
// mutation that must RED.
//
// The header is assembled in Go from the profile DB; the LLM never touches it.
// That is the whole point: an LLM asked to retype an email will eventually
// mangle it, and a wrong contact line on a resume is a silent total failure.

// TestAssembleResumeHeader pins the exact header block. It REDs on any of:
//   - M3: the #line moved above the contacts, or a second one added under the
//     name (the divider belongs under the contacts — operator's explicit call);
//   - M4: any #v value in the header block changes;
//   - M5: the contact separator or field order changes (via the embedded
//     contacts string);
//   - M6: the email loses its \@ escape (via the embedded contacts string).
func TestAssembleResumeHeader(t *testing.T) {
	contacts := buildResumeContacts("Berlin", "jane@example.com", "github.com/janedoe", "linkedin.com/in/janedoe")
	headline := buildResumeHeadline("Staff Engineer", []string{"Go", "Storage"})
	got := assembleResumeHeader("Jane Doe", headline, contacts)

	// Exact match: any single load-bearing value moving reds this, and the
	// failure diff names the line that moved.
	want := "```{=typst}\n" +
		"#text(size: 26pt, weight: \"bold\", fill: rgb(\"#0f172a\"), tracking: -0.4pt)[Jane Doe]\n" +
		"#v(2.4mm)\n" +
		"#text(size: 11pt, weight: \"semibold\", fill: rgb(\"#1e293b\"))[Staff Engineer  ·  Go  ·  Storage]\n" +
		"#linebreak()\n" +
		"#v(0.8mm)\n" +
		"#text(size: 10pt, fill: rgb(\"#64748b\"))[Berlin  ·  jane\\@example.com  ·  github.com/janedoe  ·  linkedin.com/in/janedoe]\n" +
		"#v(1.8mm)\n" +
		"#line(length: 100%, stroke: rgb(\"#cbd5e1\") + 0.7pt)\n" +
		"#v(2.0mm)\n" +
		"```\n"
	if got != want {
		t.Fatalf("assembleResumeHeader mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	// M3, structural: exactly one #line, and it sits AFTER the contacts line,
	// never under the name. A second #line under the name, or moving the
	// divider above the contacts, flips this.
	lineIdx := strings.Index(got, "#line(length:")
	contactsIdx := strings.Index(got, "#text(size: 10pt, fill: rgb(\"#64748b\"))")
	nameIdx := strings.Index(got, "tracking: -0.4pt)")
	if lineIdx < 0 || contactsIdx < 0 || nameIdx < 0 {
		t.Fatalf("header missing a load-bearing line (line=%d contacts=%d name=%d)", lineIdx, contactsIdx, nameIdx)
	}
	if strings.Count(got, "#line(length:") != 1 {
		t.Errorf("header has %d #line directives, want exactly 1 — a second divider under the name is the defect",
			strings.Count(got, "#line(length:"))
	}
	if nameIdx >= contactsIdx || contactsIdx >= lineIdx {
		t.Errorf("divider placement wrong: want name < contacts < #line, got name=%d contacts=%d line=%d",
			nameIdx, contactsIdx, lineIdx)
	}
}

// TestAssembleResumeHeader_EmptyHeadline pins the omit-when-empty behaviour:
// an empty headline omits the #text(...semibold...) line, the #v(2.4mm) gap
// before it, and the #linebreak() after it — never an empty content bracket.
// resume_scaffold relies on this for its optional headline input.
func TestAssembleResumeHeader_EmptyHeadline(t *testing.T) {
	contacts := buildResumeContacts("Berlin", "jane@example.com", "", "")
	got := assembleResumeHeader("Jane Doe", "", contacts)

	if strings.Contains(got, "semibold") {
		t.Errorf("empty headline must omit the semibold #text line — got %q", got)
	}
	if strings.Contains(got, "#linebreak()") {
		t.Errorf("empty headline must omit #linebreak() — got %q", got)
	}
	// Exactly one #v(2.4mm) would be wrong here — it must be absent.
	if strings.Contains(got, "#v(2.4mm)") {
		t.Errorf("empty headline must omit the #v(2.4mm) name→headline gap — got %q", got)
	}
	// The name line and contacts line are still present.
	if !strings.Contains(got, "Jane Doe") {
		t.Errorf("name line missing for empty headline — got %q", got)
	}
	if !strings.Contains(got, "jane\\@example.com") {
		t.Errorf("contacts line missing for empty headline — got %q", got)
	}
}

// TestBuildResumeContacts pins the contact line assembly.
//   - M5: field order (location, email, github, linkedin) and the
//     "  ·  " separator (two spaces, middle dot, two spaces);
//   - M6: the email \@ escape.
func TestBuildResumeContacts(t *testing.T) {
	t.Run("all fields in order with escaped email", func(t *testing.T) {
		got := buildResumeContacts("Berlin", "jane@example.com", "github.com/janedoe", "linkedin.com/in/janedoe")
		want := "Berlin  ·  jane\\@example.com  ·  github.com/janedoe  ·  linkedin.com/in/janedoe"
		if got != want {
			t.Fatalf("contacts = %q, want %q", got, want)
		}
		if !strings.Contains(got, "\\@") {
			t.Errorf("email lost its \\@ escape — an unescaped @ is a typst function-call sigil: %q", got)
		}
	})

	t.Run("omit empty fields, no doubled separator", func(t *testing.T) {
		got := buildResumeContacts("Berlin", "jane@example.com", "", "")
		want := "Berlin  ·  jane\\@example.com"
		if got != want {
			t.Fatalf("contacts with empty links = %q, want %q (no trailing separator, no empty slots)", got, want)
		}
		if strings.Contains(got, "  ·   ·") {
			t.Errorf("doubled separator from an empty field: %q", got)
		}
	})

	t.Run("only email escapes at, not other fields", func(t *testing.T) {
		// github/linkedin URLs carry no @ in the stored form; only the email
		// needs the sigil escape. A field that did contain an @ would also be
		// escaped, which is safe — but the canonical case is the email only.
		got := buildResumeContacts("", "name@host.io", "", "")
		if !strings.Contains(got, "name\\@host.io") {
			t.Errorf("email @ not escaped: %q", got)
		}
	})
}

// TestBuildResumeHeadline pins the per-job headline: role title plus two or
// three specialisations joined with "  ·  ", no trailing punctuation.
func TestBuildResumeHeadline(t *testing.T) {
	t.Run("role plus specialisations", func(t *testing.T) {
		got := buildResumeHeadline("Staff Engineer", []string{"Go", "Storage", "Kubernetes", "Postgres"})
		want := "Staff Engineer  ·  Go  ·  Storage  ·  Kubernetes"
		if got != want {
			t.Fatalf("headline = %q, want %q (capped at three specialisations)", got, want)
		}
	})

	t.Run("no trailing punctuation", func(t *testing.T) {
		got := buildResumeHeadline("Staff Engineer", []string{"Go"})
		if strings.HasSuffix(got, ".") || strings.HasSuffix(got, ",") {
			t.Errorf("headline has trailing punctuation: %q", got)
		}
	})

	t.Run("role only when no specialisations", func(t *testing.T) {
		got := buildResumeHeadline("Staff Engineer", nil)
		if got != "Staff Engineer" {
			t.Fatalf("headline = %q, want %q", got, "Staff Engineer")
		}
	})
}

// TestMarkdownBodyShapeGuidance pins the prompt constraints that govern the
// LLM-produced body. The header is deterministic Go output; the body shape is
// enforced only by what the prompt says, so the guard IS the prose. Each
// assertion REDs when its directive is removed.
//
//   - M1: the #### subtitle line is collapsed into its ### line (the shipped
//     defect). Guard: the guidance requires a #### line per entry.
//   - M2: a ## Summary heading is reintroduced. Guard: the guidance forbids it
//     by name.
func TestMarkdownBodyShapeGuidance(t *testing.T) {
	// M1: the two-line entry structure with a #### subtitle.
	if !strings.Contains(markdownBodyShapeGuidance, "####") {
		t.Errorf("markdownBodyShapeGuidance does not mention the #### entry subtitle — the two-line " +
			"entry structure is unguarded, and collapsing the subtitle into the ### line is the shipped defect")
	}
	// M2: the no-##-Summary directive names the heading it forbids.
	if !strings.Contains(markdownBodyShapeGuidance, "## Summary") {
		t.Errorf("markdownBodyShapeGuidance no longer names \"## Summary\" — the prohibition on a " +
			"summary heading is gone, and reintroducing one is silent")
	}
	// The LLM must not produce the header — it is assembled in Go.
	if !strings.Contains(markdownBodyShapeGuidance, "header") {
		t.Errorf("markdownBodyShapeGuidance does not tell the LLM to omit the header — the header is " +
			"assembled in Go and an LLM-produced one would duplicate it")
	}
}

// TestResumeHeaderRendersThroughPDF verifies the Go-assembled header renders
// through the production pdfrender path and the #### entry subtitle lands on
// the page. The metadata colour itself is the theme's proven invariant
// (internal/pdfrender/reference_test.go TestEntrySubtitleRuleIsLive); this
// test confirms the approved header shape does not break rendering and the
// subtitle text is extracted from the PDF.
//
// RED-on-revert: malformed typst in the header (a missing escape, a moved
// #line, a broken fence) makes PDF() return an error, or the subtitle words
// vanish from the extracted text.
func TestResumeHeaderRendersThroughPDF(t *testing.T) {
	for _, bin := range []string{"typst", "pandoc", "pdftotext"} {
		if _, err := exec.LookPath(bin); err != nil {
			if os.Getenv("CI") != "" {
				t.Fatalf("%s not on PATH in CI — preflight.yml must install it", bin)
			}
			t.Skipf("%s not on PATH — skipping render test (set CI=1 to make this fatal)", bin)
		}
	}

	contacts := buildResumeContacts("Berlin", "jane@example.com", "github.com/janedoe", "linkedin.com/in/janedoe")
	headline := buildResumeHeadline("Staff Engineer", []string{"Go", "Storage"})
	header := assembleResumeHeader("Jane Doe", headline, contacts)

	body := "## Experience\n\n" +
		"### Staff Engineer, Example Systems · 2021–Present\n\n" +
		"#### Storage and scheduling for a six-service platform\n\n" +
		"- Cut a cache promotion path from 310 ms to 0.4 ms.\n"

	md := header + "\n" + body

	adapter := pdfrender.New()
	pdfBytes, err := adapter.PDF(context.Background(), md)
	if err != nil {
		t.Fatalf("PDF() returned error on the approved header shape: %v", err)
	}
	if len(pdfBytes) == 0 {
		t.Fatal("PDF() returned empty bytes")
	}

	// Extract text and confirm both the name (header) and the subtitle (the
	// #### line the theme styles as metadata) reached the page.
	text := pdfText(t, pdfBytes)
	for _, want := range []string{"Jane", "Storage and scheduling"} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered PDF text lacks %q — the header or the #### subtitle did not land on the page", want)
		}
	}
}

// pdfText runs pdftotext on the PDF bytes and returns the extracted text.
func pdfText(t *testing.T, pdfBytes []byte) string {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/out.pdf"
	if err := os.WriteFile(path, pdfBytes, 0o600); err != nil {
		t.Fatalf("write pdf: %v", err)
	}
	out, err := exec.Command("pdftotext", path, "-").Output()
	if err != nil {
		t.Fatalf("pdftotext: %v", err)
	}
	return string(out)
}
