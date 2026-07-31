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
// testdata/resume-reference.md is the canonical shape a resume must be authored
// in. The two tests below hold the two halves of that contract:
//
//	TestThemeRulesAreExercisedByReference — every rule has content that
//	  triggers it, or an explicit record saying why it does not. Pure text
//	  comparison, no binaries, always runs.
//	TestEntrySubtitleRuleIsLive — the entry-subtitle rule survives an actual
//	  render, measured on the page rather than asserted from the source.

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
var showRuleRe = regexp.MustCompile(`(?m)^#show (.+?): (?:it =>|set )`)

// exercisedBy maps a theme selector to a substring that must appear in the
// reference markdown for that selector to fire. The substring is the markdown
// construct, not the rendered output — this test never runs typst.
var exercisedBy = map[string]string{
	"heading.where(level: 2)": "\n## ",
	"heading.where(level: 3)": "\n### ",
	"heading.where(level: 4)": "\n#### ",
	"raw.where(block: false)": "`errors.Is`",
	"table":                   "\n| Subsystem ",
	"table.cell.where(y: 0)":  "\n| --- ",
	"link":                    "](https://",
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

// Entry-subtitle geometry, measured on the deployed image (typst 0.14.2 from
// alpine, fonts-ibm-plex 6.1.1-1 from ubuntu:24.04 — the same two versions the
// Dockerfile and preflight.yml install).
//
// The rule sets the subtitle at 10pt regular in slate-500. Deleting it does NOT
// move the line and does NOT change its height: typst's default level-4 heading
// is also 10pt at this body size. What changes is the WEIGHT, and weight is
// visible only as advance width:
//
//	rule present  159.870 pt   (10pt regular)
//	rule deleted  167.180 pt   (10pt bold, typst's default heading)
//
// Both mutations were run before this test was written. The tolerance is a
// fifth of that 7.31 pt separation, so the assertion cannot pass on the bold
// rendering. Regenerate by rendering testdata/resume-reference.md through the
// theme and measuring the run below with `pdftotext -bbox`.
const (
	entrySubtitleWidthPt   = 159.870
	entrySubtitleWidthTol  = 1.5
	entrySubtitleHeightPt  = 10.0
	entrySubtitleHeightTol = 0.5
)

// entrySubtitleRun is the first `#### ` line in the reference, as pdftotext
// splits it into words.
var entrySubtitleRun = []string{"A", "tiered", "block", "cache", "for", "Go", "services"}

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
	x0, x1, height, ok := findRun(words, entrySubtitleRun)
	if !ok {
		t.Fatalf("entry subtitle %q not found in the rendered page — the reference lost its `#### ` line, "+
			"or pandoc stopped emitting a level-4 heading for it", strings.Join(entrySubtitleRun, " "))
	}

	if got := x1 - x0; !within(got, entrySubtitleWidthPt, entrySubtitleWidthTol) {
		t.Errorf("entry subtitle rendered %.3f pt wide, want %.3f ±%.1f.\n"+
			"A width near 167.2 means the level-4 rule is gone and typst is setting the line "+
			"in its default heading weight (bold) rather than the theme's regular.",
			got, entrySubtitleWidthPt, entrySubtitleWidthTol)
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
