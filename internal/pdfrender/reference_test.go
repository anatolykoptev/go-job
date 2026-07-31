package pdfrender

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The resume theme in resume.typ is a set of `#show` rules, and a rule only
// reaches the page if the markdown fed to it contains the construct that
// triggers it. That coupling has already failed once in production: the theme
// gained an entry-subtitle rule for `#### `, and the markdown the service was
// actually rendering had collapsed every entry into a single `### ` line. The
// rule was live, correct, and produced nothing. Neither the compiler, nor the
// font gauge, nor a rendered PDF reported anything wrong — the page simply
// lacked a feature nobody could see was missing.
//
// testdata/resume-reference.md pins the shape that exercises this theme, and
// the two tests below hold the two halves of that pairing:
//
//	TestThemeRulesAreExercisedByReference — every rule has content that
//	  triggers it, or an explicit record saying why it does not. Pure text
//	  comparison, no binaries, always runs.
//	TestEntrySubtitleRuleIsLive — the entry-subtitle rule survives an actual
//	  render, measured on the page rather than asserted from the source.
//
// SCOPE — read this before trusting the pair. Together they gauge the THEME:
// they prove no rule can enter resume.typ uncovered, and that the covered rules
// still reach the page. They do NOT gate the markdown that ships. The document
// TypstAdapter renders is supplied at call time by the caller of PDF(), never
// read from this fixture, so a resume authored flat — every entry collapsed
// into one `### ` line, exactly the production defect described above — still
// renders with the level-4 rule firing zero times and both tests green.
//
// Closing that half needs a check on the real payload rather than on a fixture;
// tracked in #409. Do not read a green run here as "the resumes we send are
// the approved shape".

// referenceMarkdown is the canonical resume shape. Kept deliberately generic:
// go-job is a public repository, so the fixture carries an invented person
// rather than a real resume.
func referenceMarkdown(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/resume-reference.md")
	if err != nil {
		t.Fatalf("read reference markdown: %v", err)
	}
	return string(b)
}

// showRuleRe captures the selector of every `#show <selector>: <body>` line in
// the preamble. Anchored to the line start so a `#show` inside a comment or a
// nested block body is not mistaken for a top-level rule.
//
// The separating colon cannot be found by scanning to the first one: selectors
// carry their own, as in `heading.where(level: 1)`. Every rule body begins with
// either `it =>` or `set `, so the match is anchored on that instead and the
// lazy group is forced out to the correct colon.
//
// Anchoring on the body makes the pattern FORM-SPECIFIC, and a form it does not
// know is a rule that enters the theme uncovered while this test stays green —
// the very outcome it exists to prevent. Measured misses: a lambda whose
// parameter is not named `it` (`e => …`), a bare function reference
// (`#show emph: underline`), a parenthesised parameter (`(it) => …`), a
// selector split across lines, and the show-everything form (`#show: doc => …`).
//
// showLineRe therefore counts candidate lines independently, and the test
// requires the two counts to agree. Every form above still starts at column 0,
// so an unparsed rule shows up as a count mismatch rather than as silence.
var (
	showRuleRe = regexp.MustCompile(`(?m)^#show (.+?): (?:it =>|set )`)
	showLineRe = regexp.MustCompile(`(?m)^#show[ :]`)
)

// exercisedBy maps a theme selector to a substring that must appear in the
// reference markdown for that selector to fire. The substring is the markdown
// construct, not the rendered output — this test never runs typst.
var exercisedBy = map[string]string{
	"heading.where(level: 2)": "\n## ",
	"heading.where(level: 3)": "\n### ",
	"heading.where(level: 4)": "\n#### ",
	"raw.where(block: false)": "`errors.Is`",
	// Both table rules are probed by the delimiter row rather than by a column
	// name: markdown requires a header row immediately above it, which is the
	// row `table.cell.where(y: 0)` styles. Probing "| Subsystem " instead would
	// tie theme coverage to the fixture's wording, so renaming a column would
	// report a missing theme rule.
	"table":                  "\n| --- ",
	"table.cell.where(y: 0)": "\n| --- ",
	"link":                   "](https://",
}

// knownUnexercised records the selectors the reference deliberately does not
// trigger, each with the reason. An entry here is a decision, not an oversight:
// the test fails if a selector is in neither map, so a rule cannot be added to
// the theme without someone choosing which list it belongs in.
var knownUnexercised = map[string]string{
	"heading.where(level: 1)": "a resume sets its own name via the raw typst header block, so no `# ` heading is ever emitted; the rule is kept because the theme is also reachable by documents that do use one",
	"raw.where(block: true)":  "neither a resume nor a cover letter contains a fenced code block; the ```{=typst} fence in the header is a pandoc passthrough and never becomes a typst `raw` element",
}

func TestThemeRulesAreExercisedByReference(t *testing.T) {
	t.Parallel()

	ref := referenceMarkdown(t)

	matches := showRuleRe.FindAllStringSubmatch(resumeTypstPreamble, -1)
	if len(matches) == 0 {
		t.Fatal("no #show rules found in resumeTypstPreamble — the regexp or the theme changed shape")
	}

	// Partial-parse guard. Without it a rule written in a form showRuleRe does
	// not recognise is simply absent from `matches`, and every assertion below
	// passes over a rule nobody chose to cover.
	if lines := len(showLineRe.FindAllString(resumeTypstPreamble, -1)); lines != len(matches) {
		t.Fatalf("resume.typ has %d lines starting with `#show` but showRuleRe parsed %d.\n"+
			"A rule is written in a form the selector pattern does not recognise, so it would\n"+
			"enter the theme uncovered while this test stayed green. Either rewrite it as\n"+
			"`#show <selector>: it => …` / `#show <selector>: set …`, or extend showRuleRe and\n"+
			"add the mutation that proves the extension works.", lines, len(matches))
	}

	found := make(map[string]bool, len(matches))
	for _, m := range matches {
		sel := strings.TrimSpace(m[1])
		found[sel] = true

		probe, isExercised := exercisedBy[sel]
		reason, isKnown := knownUnexercised[sel]

		switch {
		case isExercised && isKnown:
			t.Errorf("selector %q is in BOTH exercisedBy and knownUnexercised — pick one", sel)
		case isExercised:
			if !strings.Contains(ref, probe) {
				t.Errorf("theme rule %q is not exercised: reference markdown lacks %q.\n"+
					"Either restore that construct in testdata/resume-reference.md, or move the\n"+
					"selector to knownUnexercised with a reason.", sel, probe)
			}
		case isKnown:
			if reason == "" {
				t.Errorf("selector %q is listed as unexercised with an empty reason", sel)
			}
		default:
			t.Errorf("theme rule %q is covered by nothing.\n"+
				"Add a probe to exercisedBy (and the construct to the reference), or record it\n"+
				"in knownUnexercised with the reason it cannot be triggered. A rule with no\n"+
				"content to trigger it renders nothing and reports no error.", sel)
		}
	}

	// A stale entry is its own defect: it claims coverage for a rule that no
	// longer exists, and it hides the fact that the rule was removed.
	for sel := range exercisedBy {
		if !found[sel] {
			t.Errorf("exercisedBy names selector %q, which is not in the theme any more", sel)
		}
	}
	for sel := range knownUnexercised {
		if !found[sel] {
			t.Errorf("knownUnexercised names selector %q, which is not in the theme any more", sel)
		}
	}
}

// The rule sets the entry subtitle at 10pt regular in slate-500. Deleting it
// neither moves the line nor changes its height: typst's default level-4
// heading is also 10pt at this body size. Only the WEIGHT changes, and weight
// is visible solely as advance width.
//
// The assertion is a RATIO against a control run, not an absolute width. An
// absolute pt value depends on three inputs this test does not pin — typst's
// shaper, the fonts-ibm-plex metrics, and poppler's bbox emitter — so a font
// package revision or a typst kerning change would red the gate while
// reporting "the level-4 rule is gone", a diagnosis that is both wrong and
// expensive to chase. The control is set in the same 10pt regular face, so any
// uniform metric change moves numerator and denominator together and cancels.
// Only a change to the subtitle's own styling moves the ratio.
//
// Measured on the deployed image (typst 0.14.2, fonts-ibm-plex 6.1.1-1):
//
//	                          subtitle   control    ratio
//	rule as written            159.870    56.780   2.8156
//	weight flipped to bold     167.180    56.780   2.9443
//	size raised to 11pt        175.857    56.780   3.0972
//
// The control held at 56.780 under both mutations. Tolerance is a fifth of the
// nearest separation (0.1287), so no mutation above can pass. Regenerate by
// rendering testdata/resume-reference.md through the theme and measuring both
// runs with `pdftotext -bbox`.
const (
	entrySubtitleRatio    = 2.8156
	entrySubtitleRatioTol = 0.025

	// Height is the size guard the ratio alone does not isolate: it reds on the
	// 11pt mutation above (11.00 measured) and is unmoved by the weight flip.
	entrySubtitleHeightPt  = 10.0
	entrySubtitleHeightTol = 0.5
)

// entrySubtitleRun is the first `#### ` line in the reference, and controlRun a
// fragment of the header contact line, as pdftotext splits them into words.
//
// The control is deliberately taken from the fixture's own raw-typst header
// rather than from theme-set body text: it is 10pt regular there by a literal
// in the fixture, so it moves only when the fixture is edited on purpose, not
// when the theme's body size is retuned.
var (
	entrySubtitleRun = []string{"A", "tiered", "block", "cache", "for", "Go", "services"}
	controlRun       = []string{"Portland,", "OR"}
)

func TestEntrySubtitleRuleIsLive(t *testing.T) {
	requireRenderBinaries(t)

	a := New()
	if !a.Ready() {
		t.Fatal("TypstAdapter.Ready() returned false although typst and pandoc are on PATH — " +
			"the font probe failed; see gojob_pdf_font_available")
	}

	pdfBytes, err := a.PDF(context.Background(), referenceMarkdown(t))
	if err != nil {
		t.Fatalf("PDF(): %v", err)
	}

	words := pdfWords(t, pdfBytes)

	subX0, subX1, height, ok := findRun(words, entrySubtitleRun)
	if !ok {
		t.Fatalf("entry subtitle %q not found in the rendered page — the reference lost its `#### ` line, "+
			"or pandoc stopped emitting a level-4 heading for it", strings.Join(entrySubtitleRun, " "))
	}
	ctlX0, ctlX1, _, ok := findRun(words, controlRun)
	if !ok {
		t.Fatalf("control run %q not found in the rendered page — the fixture's header changed, so the "+
			"ratio below has no denominator", strings.Join(controlRun, " "))
	}

	subWidth, ctlWidth := subX1-subX0, ctlX1-ctlX0
	if ctlWidth <= 0 {
		t.Fatalf("control run measured %.3f pt wide; cannot form a ratio", ctlWidth)
	}

	if got := subWidth / ctlWidth; !within(got, entrySubtitleRatio, entrySubtitleRatioTol) {
		t.Errorf("entry subtitle is %.4f× the control run, want %.4f ±%.4f "+
			"(subtitle %.3f pt, control %.3f pt).\n"+
			"A ratio near 2.944 means the level-4 rule is gone or asks for bold, and typst is "+
			"setting the line in heading weight rather than the theme's regular. A ratio that "+
			"moved while the control also moved is a toolchain change, not a theme change — "+
			"check typst and fonts-ibm-plex versions before editing the constant.",
			got, entrySubtitleRatio, entrySubtitleRatioTol, subWidth, ctlWidth)
	}
	if !within(height, entrySubtitleHeightPt, entrySubtitleHeightTol) {
		t.Errorf("entry subtitle rendered %.2f pt tall, want %.1f ±%.1f — the rule's size changed",
			height, entrySubtitleHeightPt, entrySubtitleHeightTol)
	}
}

func within(got, want, tol float64) bool {
	d := got - want
	if d < 0 {
		d = -d
	}
	return d <= tol
}

// requireRenderBinaries fails rather than skips under CI. A render test that
// skips when its dependency is absent is green over zero coverage, which is how
// the pre-existing end-to-end PDF test ran for months without executing.
func requireRenderBinaries(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"typst", "pandoc", "pdftotext"} {
		if _, err := exec.LookPath(bin); err != nil {
			if os.Getenv("CI") != "" {
				t.Fatalf("%s is not on PATH in CI — preflight.yml must install it; "+
					"skipping here would report green over a test that never ran", bin)
			}
			t.Skipf("%s not on PATH — skipping render test (set CI=1 to make this fatal)", bin)
		}
	}
}

var bboxWordRe = regexp.MustCompile(
	`<word xMin="([0-9.]+)" yMin="([0-9.]+)" xMax="([0-9.]+)" yMax="([0-9.]+)">([^<]*)</word>`)

type pdfWord struct {
	xMin, yMin, xMax, yMax float64
	text                   string
}

// pdfWords renders page 1 to pdftotext's bbox XML and parses the word boxes.
func pdfWords(t *testing.T, pdfBytes []byte) []pdfWord {
	t.Helper()

	dir := t.TempDir()
	path := dir + "/out.pdf"
	if err := os.WriteFile(path, pdfBytes, 0o600); err != nil {
		t.Fatalf("write pdf: %v", err)
	}

	out, err := exec.Command("pdftotext", "-bbox", "-f", "1", "-l", "1", path, "-").Output()
	if err != nil {
		t.Fatalf("pdftotext -bbox: %v", err)
	}

	var words []pdfWord
	for _, m := range bboxWordRe.FindAllStringSubmatch(string(out), -1) {
		var w pdfWord
		for i, dst := range []*float64{&w.xMin, &w.yMin, &w.xMax, &w.yMax} {
			v, err := strconv.ParseFloat(m[i+1], 64)
			if err != nil {
				t.Fatalf("parse bbox coordinate %q: %v", m[i+1], err)
			}
			*dst = v
		}
		w.text = m[5]
		words = append(words, w)
	}
	if len(words) == 0 {
		t.Fatal("pdftotext -bbox returned no words for page 1")
	}
	return words
}

// findRun locates a contiguous sequence of words and returns the run's left
// edge, right edge and the height of its first box.
func findRun(words []pdfWord, run []string) (x0, x1, height float64, ok bool) {
	if len(run) == 0 || len(words) < len(run) {
		return 0, 0, 0, false
	}
	for i := 0; i+len(run) <= len(words); i++ {
		match := true
		for j, want := range run {
			if words[i+j].text != want {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		first, last := words[i], words[i+len(run)-1]
		return first.xMin, last.xMax, first.yMax - first.yMin, true
	}
	return 0, 0, 0, false
}
