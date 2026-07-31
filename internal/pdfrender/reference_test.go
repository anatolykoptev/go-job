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
//
// The fixture stays an invented person permanently — it is a gauge for the
// theme, not a copy of anyone's resume, and this repository is public. It is
// expected to track the theme's rules rather than converge on production
// content; #409 owns the production side.

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
// requires the two counts to agree, so an unparsed rule shows up as a count
// mismatch rather than as silence.
//
// showLineRe tolerates leading whitespace on purpose. Anchoring it at column 0
// left a hole exactly one keystroke wide: an INDENTED `#show` matched neither
// pattern, so `  #show heading.where(level: 4): it => text(weight: "bold", …)`
// — a rule that genuinely restyles the page — entered the theme with the count
// check green. Top-level rules in this theme are column-0 by convention, so an
// indented one is a hard error rather than a form to support: it will red the
// count check, and the fix is to unindent it, not to extend showRuleRe.
var (
	showRuleRe = regexp.MustCompile(`(?m)^#show (.+?): (?:it =>|set )`)
	showLineRe = regexp.MustCompile(`(?m)^[ \t]*#show[ :]`)
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
		t.Fatalf("resume.typ has %d `#show` lines but showRuleRe parsed %d.\n"+
			"A rule is written in a form the selector pattern does not recognise, so it would\n"+
			"enter the theme uncovered while this test stayed green. If it is indented, unindent\n"+
			"it — top-level rules are column-0 here. Otherwise rewrite it as\n"+
			"`#show <selector>: it => …` / `#show <selector>: set …`, or extend showRuleRe and\n"+
			"add the mutation that proves the extension works.", lines, len(matches))
	}

	// resumeTypstPreamble is not the only typst the renderer assembles:
	// adapter.go prepends ligaPreamble to every document. A `#show` added there
	// would reach every PDF with no coverage and no count mismatch, because
	// everything above reads the theme alone. Nothing needs it today, so assert
	// that stays true rather than teaching the coverage maps a second source.
	if n := len(showLineRe.FindAllString(ligaPreamble, -1)); n != 0 {
		t.Errorf("ligaPreamble now carries %d `#show` rule(s). It is injected into every rendered\n"+
			"document but is not scanned by the coverage maps above, so those rules would style\n"+
			"real output with nothing gating them.\n"+
			"Do not move them into resume.typ reflexively: ligaPreamble is injected AFTER the theme\n"+
			"preamble precisely so it wins the cascade, and a rule that needs that position would\n"+
			"change precedence on the way. If it must stay, extend this test to scan both sources\n"+
			"and give the new rule its entry in exercisedBy or knownUnexercised.", n)
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
//	rule as written            159.870   107.770   1.4834
//	weight flipped to bold     167.180   107.770   1.5513
//	size raised to 11pt        175.857   107.770   1.6317
//
// The control held at 107.770 under both mutations. Tolerance is a fifth of the
// nearest separation (0.0678), so no mutation above can pass. Regenerate by
// rendering testdata/resume-reference.md through the theme and measuring both
// runs with `pdftotext -bbox`.
//
// The control is asserted too, with a deliberately loose band. It is what makes
// the failure message able to tell the two causes apart: without it the test
// knows only that the ratio moved, and the error text would blame the level-4
// rule for a theme-wide font change. Measured: swapping the theme's body font
// to IBM Plex Mono moves the ratio to 3.0000 with the level-4 rule untouched.
const (
	entrySubtitleRatio    = 1.4834
	entrySubtitleRatioTol = 0.013

	// Loose on purpose — this is a cause-attribution signal, not a second
	// geometry gate. It should survive ordinary rounding and red only on a real
	// change of face or size.
	controlWidthPt  = 107.770
	controlWidthTol = 5.4 // ±5%

	// Height is the size guard the ratio alone does not isolate: it reds on the
	// 11pt mutation above (11.00 measured) and is unmoved by the weight flip.
	entrySubtitleHeightPt  = 10.0
	entrySubtitleHeightTol = 0.5
)

// entrySubtitleRun is the first `#### ` line in the reference; controlRun is the
// address on the header contact line, as pdftotext splits them into words.
//
// The control is deliberately taken from the fixture's own raw-typst header
// rather than from theme-set body text: it is 10pt regular there by a literal in
// the fixture, so it moves only when the fixture is edited on purpose, not when
// the theme's body size or leading is retuned.
//
// It is also unique in the whole document, and measureRun asserts that rather
// than taking the first hit. An earlier control, "Portland, OR", appeared three
// times in the fixture; it read as unambiguous only because the duplicates fell
// on page 2 while the scan was restricted to page 1, so a fixture that later
// shrank to one page would have measured an 11pt bold heading as the
// denominator and produced a plausible, wrong ratio. The scan now covers every
// page, which removes the dependency on where the fixture happens to break.
var (
	entrySubtitleRun = []string{"A", "tiered", "block", "cache", "for", "Go", "services"}
	controlRun       = []string{"jordan@example.invalid"}
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

	subWidth, height := measureRun(t, words, entrySubtitleRun, "entry subtitle",
		"the reference lost its `#### ` line, or pandoc stopped emitting a level-4 heading for it")
	ctlWidth, _ := measureRun(t, words, controlRun, "control run",
		"the fixture's header contact line changed, so the ratio has no denominator")

	// Attribute the cause before reporting it. A control inside its band means
	// the face the page is set in did not move, so a ratio change is the
	// subtitle's own styling; a control outside it means the toolchain or the
	// fixture header moved and the ratio constant is what needs re-measuring.
	controlHeld := within(ctlWidth, controlWidthPt, controlWidthTol)
	if !controlHeld {
		t.Errorf("control run measured %.3f pt, want %.3f ±%.1f.\n"+
			"The page is not set in the face this test was calibrated against, so the ratio below "+
			"cannot be read as a statement about the level-4 rule. Check typst and fonts-ibm-plex "+
			"versions and whether the fixture's header was edited, then re-measure both constants.",
			ctlWidth, controlWidthPt, controlWidthTol)
	}

	if got := subWidth / ctlWidth; !within(got, entrySubtitleRatio, entrySubtitleRatioTol) {
		cause := "the level-4 rule is gone or asks for a heavier weight, and typst is setting the " +
			"line in heading weight rather than the theme's regular"
		if !controlHeld {
			cause = "the control moved too, so this is a toolchain or fixture change rather than a " +
				"theme change — do not go looking at the level-4 rule first"
		}
		t.Errorf("entry subtitle is %.4f× the control run, want %.4f ±%.4f "+
			"(subtitle %.3f pt, control %.3f pt).\n%s.",
			got, entrySubtitleRatio, entrySubtitleRatioTol, subWidth, ctlWidth, cause)
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
	page                   int // 1-based; a run must not span two of these
	xMin, yMin, xMax, yMax float64
	text                   string
}

// pdfWords parses pdftotext's bbox XML for the WHOLE document.
//
// Every page, not just the first, so that measureRun's uniqueness check means
// what it says. Restricting the scan to page 1 made uniqueness depend on where
// the fixture happens to break: a duplicate run sitting on page 2 was invisible,
// and a fixture that later shrank to one page would have brought it into range
// and silently supplied the wrong denominator.
//
// Each word carries its page, and findRuns requires a run to sit on one. Without
// that the flat slice joins the last word of a page to the first of the next,
// and a two-word run spanning the seam matches a phrase that appears on no page
// at all. Measured on this fixture: `min` (page 1, y=720.8) followed by
// `Admission` (page 2, y=55.6) matched as a run and measured -123.835 pt wide,
// because box coordinates are page-local and the second word's x sits left of
// the first's. A left-to-right seam would have produced a plausible width
// instead of a negative one, reported through the ratio as a verdict on the
// level-4 rule.
func pdfWords(t *testing.T, pdfBytes []byte) []pdfWord {
	t.Helper()

	dir := t.TempDir()
	path := dir + "/out.pdf"
	if err := os.WriteFile(path, pdfBytes, 0o600); err != nil {
		t.Fatalf("write pdf: %v", err)
	}

	out, err := exec.Command("pdftotext", "-bbox", path, "-").Output()
	if err != nil {
		t.Fatalf("pdftotext -bbox: %v", err)
	}

	// Split on the page element so each word can be tagged with the page it was
	// laid out on. Segment 0 is the XML header, before any page.
	segments := strings.Split(string(out), "<page")
	if len(segments) < 2 {
		t.Fatalf("pdftotext -bbox emitted no <page> element; got %d bytes", len(out))
	}

	var words []pdfWord
	for page, segment := range segments[1:] {
		for _, m := range bboxWordRe.FindAllStringSubmatch(segment, -1) {
			w := pdfWord{page: page + 1}
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
	}
	if len(words) == 0 {
		t.Fatal("pdftotext -bbox returned no words")
	}
	return words
}

// measureRun returns the width and first-box height of the single occurrence of
// run, and fails when there is not exactly one.
//
// Uniqueness is asserted rather than assumed: taking the first match silently
// measures whichever copy happens to come first, and a run that appears twice in
// two different faces would yield a plausible number from the wrong one.
//
// name labels the run; absentHint explains what an absence means and is used
// only on the not-found branch. The two failures have different causes and must
// not share prose — a duplicated run is not a missing one, and telling the
// reader to go look at the same place for both is how a diagnosis gets wasted.
func measureRun(t *testing.T, words []pdfWord, run []string, name, absentHint string) (width, height float64) {
	t.Helper()

	at := findRuns(words, run)
	switch len(at) {
	case 1:
		// expected
	case 0:
		t.Fatalf("%s %q not found in the rendered document — %s", name, strings.Join(run, " "), absentHint)
	default:
		t.Fatalf("%s %q occurs %d times in the rendered document; a measurement anchored on it "+
			"could silently take the wrong copy. Restore its uniqueness in the fixture, or "+
			"anchor on a run that appears once.", name, strings.Join(run, " "), len(at))
	}

	first, last := words[at[0]], words[at[0]+len(run)-1]
	width, height = last.xMax-first.xMin, first.yMax-first.yMin

	// Belt for anything findRuns' page check does not already exclude: a width
	// that is not positive means the run was measured across two coordinate
	// systems, and every number derived from it is meaningless rather than
	// merely wrong.
	if width <= 0 {
		t.Fatalf("%s %q measured %.3f pt wide — the run does not read left to right, so it was not "+
			"laid out as one line", name, strings.Join(run, " "), width)
	}

	// height is the FIRST box's, which is the size of the face the run starts
	// in. Both runs measured here are single-line by construction; a run that
	// wrapped would report only its first line's height.
	return width, height
}

// TestFindRunsRejectsPageStraddle is the falsification for the page field, and
// it needs no render: the seam is trivial to construct and impossible to
// construct reliably by editing the fixture.
//
// Drop the `page` comparison in findRuns and this goes red. That is the whole
// point — the words below are adjacent in the slice and on no page together, so
// a matcher that ignores pages reports a phrase the document does not contain
// and hands measureRun two coordinate systems to subtract.
func TestFindRunsRejectsPageStraddle(t *testing.T) {
	t.Parallel()

	words := []pdfWord{
		{page: 1, xMin: 100, xMax: 130, yMin: 700, yMax: 710, text: "per"},
		{page: 1, xMin: 379.9, xMax: 396.8, yMin: 720.8, yMax: 730.8, text: "min"},
		{page: 2, xMin: 209.1, xMax: 256.0, yMin: 55.6, yMax: 65.6, text: "Admission"},
		{page: 2, xMin: 260, xMax: 300, yMin: 55.6, yMax: 65.6, text: "decisions"},
	}

	if at := findRuns(words, []string{"min", "Admission"}); len(at) != 0 {
		t.Errorf("findRuns matched %q across a page break at %v; the run exists on no page, and "+
			"measuring it subtracts page-2 coordinates from page-1 ones", "min Admission", at)
	}
	// Same-page runs still match, so the guard is not simply refusing everything.
	if at := findRuns(words, []string{"Admission", "decisions"}); len(at) != 1 {
		t.Errorf("findRuns found %d occurrences of a genuine same-page run, want 1", len(at))
	}
}

// findRuns returns the start index of every contiguous occurrence of run that
// lies entirely on one page. A match straddling a page break is discarded: the
// words are adjacent only in this flat slice, never on any page, and measuring
// it mixes two coordinate systems.
func findRuns(words []pdfWord, run []string) []int {
	if len(run) == 0 || len(words) < len(run) {
		return nil
	}
	var at []int
	for i := 0; i+len(run) <= len(words); i++ {
		match := true
		for j, want := range run {
			if words[i+j].text != want || words[i+j].page != words[i].page {
				match = false
				break
			}
		}
		if match {
			at = append(at, i)
		}
	}
	return at
}
