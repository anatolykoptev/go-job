package pdfrender

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/anatolykoptev/go-kit/render/typst"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
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
// The oracle is IDENTITY with the embedded file, not a string literal copied
// out of it. An earlier version asserted "17.8mm present, 16mm absent"; those
// pin the design rather than the registration, so a future retune of the margin
// would red this test with the message "template is not registered", which is
// false. Identity survives any design edit and still fails the moment the
// resolved theme is not ours.
//
// The second assertion keeps the test from going vacuous: it establishes that
// the built-in this registration displaces is genuinely different. Without it,
// a go-kit release that happened to ship our preamble would leave the identity
// check green with nothing registered at all.
//
// Falsification: comment out the RegisterTheme call in init(), or register
// under a different Name → the built-in is returned → identity fails.
func TestThemeRegistration(t *testing.T) {
	t.Parallel()

	theme, ok := typst.LookupTheme("resume")
	if !ok {
		t.Fatal("typst.LookupTheme(\"resume\") returned ok=false — no theme registered under \"resume\"")
	}

	if theme.Preamble != resumeTypstPreamble {
		t.Errorf("resume theme is not go-job's embedded template — got %d bytes, want %d.\nresolved first 200 chars: %q",
			len(theme.Preamble), len(resumeTypstPreamble), theme.Preamble[:min(200, len(theme.Preamble))])
	}

	builtIn, ok := typst.LookupTheme(builtInProbeTheme)
	if !ok {
		t.Fatalf("typst.LookupTheme(%q) returned ok=false — go-kit's built-in set is not registered, so this test cannot tell an override from a coincidence", builtInProbeTheme)
	}
	if builtIn.Preamble == resumeTypstPreamble {
		t.Errorf("go-kit's %q preamble is byte-identical to our embedded template — the identity assertion above proves nothing", builtInProbeTheme)
	}
}

// builtInProbeTheme is a go-kit built-in that go-job does NOT override, used to
// prove the registry holds something other than our own file.
const builtInProbeTheme = "report"

// TestMissingFontFamilies guards the detector for the failure this package's
// font handling exists to close. typst substitutes a missing family without
// erroring, so the render still succeeds and the resume still looks like a
// resume — only in the wrong face. Nothing else in the process notices.
//
// The captured input is the real `typst fonts` output from the running go-job
// container on 2026-07-31, which had typst 0.14.2, pandoc 3.10, and no IBM Plex.
//
// Falsification: make missingFontFamilies return nil unconditionally → the
// "container before the fix" case fails.
func TestMissingFontFamilies(t *testing.T) {
	t.Parallel()

	const containerBeforeFix = "DejaVu Sans Mono\nLibertinus Serif\nNew Computer Modern\nNew Computer Modern Math\n"
	// Weight variants list as separate families; the plain family is what the
	// preamble names, and only its absence matters.
	const variantsOnly = "DejaVu Sans Mono\nIBM Plex Sans SmBld\nIBM Plex Sans Thai\nIBM Plex Mono Text\n"
	const afterFix = "DejaVu Sans Mono\nIBM Plex Mono\nIBM Plex Mono SmBld\nIBM Plex Sans\nIBM Plex Sans SmBld\nLibertinus Serif\n"

	for _, tc := range []struct {
		name string
		list string
		want []string
	}{
		{"container before the fix", containerBeforeFix, []string{"IBM Plex Sans", "IBM Plex Mono"}},
		{"only weight variants present", variantsOnly, []string{"IBM Plex Sans", "IBM Plex Mono"}},
		{"after the fix", afterFix, nil},
		{"empty output", "", []string{"IBM Plex Sans", "IBM Plex Mono"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := missingFontFamilies([]byte(tc.list), requiredFontFamilies)
			if len(got) != len(tc.want) {
				t.Fatalf("missing = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("missing[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// gaugeValue reads a gauge's current value without prometheus/testutil, which
// is not vendored.
func gaugeValue(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		t.Fatalf("gauge Write: %v", err)
	}
	return m.GetGauge().GetValue()
}

// TestReadySetsFontGauge guards the DECISION, not the parse. Review deleted the
// whole probe block out of Ready() and the package stayed green: every earlier
// mutation targeted missingFontFamilies and requiredFontFamilies, so the parse
// was pinned while the branch that turns a parse into a gauge value was not.
// An inverted Set, a dropped case or a removed call all shipped silently — the
// detector for a silent failure was itself silently deletable.
//
// Drives Ready() through an injected lister, so it reaches the decision with or
// without a typst binary on the box and never skips.
//
// Not parallel: the gauges are package-level.
func TestReadySetsFontGauge(t *testing.T) {
	const complete = "IBM Plex Sans\nIBM Plex Mono\nDejaVu Sans Mono\n"
	const containerBeforeFix = "DejaVu Sans Mono\nLibertinus Serif\n"

	for _, tc := range []struct {
		name string
		list string
		err  error
		want float64
	}{
		{"every required face present", complete, nil, 1},
		{"the pre-fix container", containerBeforeFix, nil, 0},
		{"typst could not be run", "", errors.New("exec: typst: not found"), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pdfFontAvailableGauge.Set(-1) // poison, so a missing Set is visible
			a := New()
			a.fontLister = func(context.Context) ([]byte, error) {
				return []byte(tc.list), tc.err
			}

			a.Ready()

			if got := gaugeValue(t, pdfFontAvailableGauge); got != tc.want {
				t.Errorf("gojob_pdf_font_available = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCheckFontsDiagnostic pins what the OPERATOR is told, which the gauge
// cannot express. "typst would not run" and "typst ran, the faces are absent"
// are both correctly 0, but they send someone to different places — and with
// the error branch swallowed the second message is emitted for the first
// condition, which is 0 by the wrong road.
//
// It also pins the LEVEL. WITH_PDF=0 is the Dockerfile default, so an Error
// there fires on every boot of a supported build and devalues the Error that
// means something.
//
// Drives checkFonts directly rather than Ready: Ready derives typstPresent from
// the real PATH, which would make the level assertion depend on whether the box
// running the test happens to have typst.
//
// Not parallel: gauges and slog.Default are package/global.
func TestCheckFontsDiagnostic(t *testing.T) {
	const complete = "IBM Plex Sans\nIBM Plex Mono\n"
	listerErr := errors.New("exec: typst: not found")

	for _, tc := range []struct {
		name         string
		list         string
		err          error
		typstPresent bool
		wantLevel    string // "" = nothing logged at Warn or above
		wantMsg      string
	}{
		{"all faces present", complete, nil, true, "", ""},
		{"faces absent from the image", "DejaVu Sans Mono\n", nil, true, "ERROR", "are absent from this image"},
		{"typst present but unreadable", "", listerErr, true, "ERROR", "could not be read"},
		{"typst absent, a WITH_PDF=0 build", "", listerErr, false, "WARN", "expected on a WITH_PDF=0 build"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logged bytes.Buffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelWarn})))
			defer slog.SetDefault(prev)

			a := New()
			a.fontLister = func(context.Context) ([]byte, error) { return []byte(tc.list), tc.err }
			a.checkFonts(context.Background(), tc.typstPresent)

			out := logged.String()
			if tc.wantLevel == "" {
				if out != "" {
					t.Errorf("nothing should be reported, got: %s", out)
				}
				return
			}
			if !strings.Contains(out, "level="+tc.wantLevel) {
				t.Errorf("want level=%s, got: %s", tc.wantLevel, out)
			}
			// Upper bound, not just a lower one: without this, a branch that
			// emits its Warn AND an Error still passes, which is the boot-noise
			// regression the level split exists to prevent.
			if tc.wantLevel == "WARN" && strings.Contains(out, "level=ERROR") {
				t.Errorf("WITH_PDF=0 is a supported build and must not raise an Error: %s", out)
			}
			if !strings.Contains(out, tc.wantMsg) {
				t.Errorf("message does not mention %q — the operator is pointed at the wrong problem.\ngot: %s", tc.wantMsg, out)
			}
		})
	}
}

// TestTypstBinary pins the precedence go-kit's resolveEnvOrPath uses. Probing a
// different installation than the renderer runs would measure the wrong font
// set and report it as fact.
func TestTypstBinary(t *testing.T) {
	for _, tc := range []struct {
		name           string
		render, vaelor string
		want           string
	}{
		{"neither set falls back to PATH", "", "", "typst"},
		{"RENDER wins", "/opt/a/typst", "/opt/b/typst", "/opt/a/typst"},
		{"legacy VAELOR is honoured alone", "", "/opt/b/typst", "/opt/b/typst"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RENDER_TYPST_PATH", tc.render)
			t.Setenv("VAELOR_TYPST_PATH", tc.vaelor)
			if got := typstBinary(); got != tc.want {
				t.Errorf("typstBinary() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRequiredFontFamiliesMatchPreamble keeps requiredFontFamilies honest: a
// preamble that starts naming a face nobody checks for reopens the silent
// substitution the gauge exists to detect.
func TestRequiredFontFamiliesMatchPreamble(t *testing.T) {
	t.Parallel()

	for _, family := range requiredFontFamilies {
		if !strings.Contains(resumeTypstPreamble, `font: "`+family+`"`) {
			t.Errorf("requiredFontFamilies lists %q but resume.typ never names it — the check guards a face the theme does not use", family)
		}
	}
	// And the reverse: every font: "..." in the preamble must be checked.
	for _, chunk := range strings.Split(resumeTypstPreamble, `font: "`)[1:] {
		end := strings.Index(chunk, `"`)
		if end < 0 {
			t.Errorf(`resume.typ has an unterminated font: " — the preamble is malformed: %q`, chunk[:min(60, len(chunk))])
			continue
		}
		named := chunk[:end]
		found := false
		for _, family := range requiredFontFamilies {
			if family == named {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("resume.typ names font %q but requiredFontFamilies does not check for it — its absence would substitute silently", named)
		}
	}
}

// TestIsNoBinaryErr verifies that isNoBinaryErr classifies errors via the
// typst.ErrBinaryNotFound sentinel, NOT by substring matching. This is the
// silent-failure surface in the direction that matters: a false positive
// reports a real render failure as "binary absent" and degrades the whole
// request to md-only with a Warn, not an Error.
//
// Two directions:
//   - An error wrapping typst.ErrBinaryNotFound → true (binary absent).
//   - A plain typst compile failure whose text happens to contain "binary
//     not found" → false (real failure, NOT binary-absent). This is the
//     case the substring check misclassifies.
//
// Falsification: put the substring check back (strings.Contains(err.Error(),
// "binary not found")) → the compile-failure case goes RED (substring matches
// even though the error is not a binary-absent sentinel).
func TestIsNoBinaryErr(t *testing.T) {
	t.Parallel()

	// Direction 1: error wrapping the sentinel → true.
	sentinelErr := fmt.Errorf("typst: %w (set RENDER_TYPST_PATH or ensure typst is on PATH)", typst.ErrBinaryNotFound)
	if !isNoBinaryErr(sentinelErr) {
		t.Error("isNoBinaryErr returned false for an error wrapping typst.ErrBinaryNotFound — should be true")
	}

	// Direction 2: plain compile failure whose text contains "binary not found"
	// but does NOT wrap the sentinel → false. This is the false-positive case
	// the substring check misclassifies.
	compileErr := errors.New("typst compile: exit status 1\nstderr: error: binary not found in source")
	if isNoBinaryErr(compileErr) {
		t.Error("isNoBinaryErr returned true for a plain compile failure whose text contains 'binary not found' — should be false (not a binary-absent sentinel)")
	}

	// Direction 3: nil error → false.
	if isNoBinaryErr(nil) {
		t.Error("isNoBinaryErr returned true for nil — should be false")
	}

	// Direction 4: unrelated error → false.
	if isNoBinaryErr(errors.New("some other error")) {
		t.Error("isNoBinaryErr returned true for an unrelated error — should be false")
	}
}

// TestReadyLooksUpTheResolvedBinary pins that Ready resolves the SAME typst the
// font probe and the renderer use, rather than a literal "typst".
//
// Not hypothetical: with RENDER_TYPST_PATH pointing at an out-of-PATH install, a
// bare lookup reports gojob_pdf_renderer_available 0 while gojob_pdf_font_available
// reads that install's fonts and reports 1. An operator told "no renderer, but
// its fonts are fine" learns nothing true.
//
// Stubs the lookup entirely, so the assertion does not depend on what happens to
// be installed on the box running the test — the earlier reason this was left
// unguarded.
func TestReadyLooksUpTheResolvedBinary(t *testing.T) {
	const pinned = "/opt/pinned/typst"
	t.Setenv("RENDER_TYPST_PATH", pinned)
	t.Setenv("VAELOR_TYPST_PATH", "")

	var asked []string
	a := New()
	a.lookPath = func(name string) (string, error) {
		asked = append(asked, name)
		return name, nil
	}
	a.fontLister = func(context.Context) ([]byte, error) {
		return []byte("IBM Plex Sans\nIBM Plex Mono\n"), nil
	}

	a.Ready()

	var sawPinned bool
	for _, name := range asked {
		if name == pinned {
			sawPinned = true
		}
		if name == "typst" {
			t.Errorf("Ready looked up the literal %q while RENDER_TYPST_PATH pinned %q — the renderer gauge would disagree with the font gauge", name, pinned)
		}
	}
	if !sawPinned {
		t.Errorf("Ready never looked up the pinned binary %q; it asked for %v", pinned, asked)
	}
}

// TestZeroValueAdapterReady pins that an adapter built as a literal rather than
// through New still resolves a lookup instead of panicking on a nil field.
func TestZeroValueAdapterReady(t *testing.T) {
	a := &TypstAdapter{fontLister: func(context.Context) ([]byte, error) {
		return []byte("IBM Plex Sans\nIBM Plex Mono\n"), nil
	}}
	a.Ready() // must not panic
}
