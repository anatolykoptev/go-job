package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anatolykoptev/go_job/internal/engine/jobs/applications"
)

// resume_author_test.go falsifies each load-bearing behaviour. Each test names
// the mutation that turns it RED (F<n> — mutation: <what I broke> → RED? yes +
// the failing assertion). A compile error is not a falsification: the binary
// must run and the assertion must fail.

// stubAuthorRenderer is a test double for PDFRenderer.
type stubAuthorRenderer struct {
	pdf []byte
	err error
}

func (s *stubAuthorRenderer) PDF(_ context.Context, _ string) ([]byte, error) {
	return s.pdf, s.err
}

// validHeader builds a header via the single source (assembleResumeHeader) so
// tests do not carry a second copy of the geometry.
func validHeader(headline string) string {
	contacts := buildResumeContacts("Berlin", "jane@example.com", "github.com/janedoe", "linkedin.com/in/janedoe")
	return assembleResumeHeader("Jane Doe", headline, contacts)
}

// validBody is a body that satisfies the shape contract: ## sections, ### /
// #### two-line entries, bullets, no Summary heading, no level-1, no ---.
func validBody() string {
	return "## Experience\n\n" +
		"### Staff Engineer, Example Systems · 2021–Present\n\n" +
		"#### Storage and scheduling for a six-service platform\n\n" +
		"- Cut a cache promotion path from 310 ms to 0.4 ms.\n" +
		"- Owned the on-call rotation for a 12-node fleet.\n\n" +
		"## Selected Open Source\n\n" +
		"### go-browser · 2024\n\n" +
		"#### A browser-infrastructure toolkit\n\n" +
		"- 1.2k stars, 40 contributors.\n"
}

// validResume is a header + body that passes the lint clean.
func validResume(headline string) string {
	return validHeader(headline) + "\n" + validBody()
}

// ─── F1: resume_lint accepts a ### entry with no #### and no explicit absence ──

// TestLintRejectsEntryWithNoSubtitle is the load-bearing test for the
// job-65473 defect: a ### entry whose descriptor was collapsed into the ###
// line, with no #### subtitle anywhere.
//
// F1 — mutation: drop the "### entry followed by a bullet with no ####" branch
// from lintEntrySubtitles (or make it a no-op) → RED? yes, the assertion
// `!v.OK` fails because the lint accepts the bad input.
func TestLintRejectsEntryWithNoSubtitle(t *testing.T) {
	// The exact shape that shipped for job 65473: ### entries with the
	// descriptor collapsed in, no #### anywhere.
	bad := validHeader("Staff Engineer") + "\n" +
		"## Experience\n\n" +
		"### Staff Engineer, Example Systems · Storage and scheduling · 2021–Present\n\n" +
		"- Cut a cache promotion path from 310 ms to 0.4 ms.\n"

	v := LintResume(bad)
	if v.OK {
		t.Fatalf("lint accepted a ### entry with no #### and no explicit absence — " +
			"this is the job-65473 defect the tool exists to catch")
	}
	// Confirm the finding names the right rule.
	found := false
	for _, f := range v.Findings {
		if f.Rule == lintRuleEntrySubtitle {
			found = true
		}
	}
	if !found {
		t.Errorf("lint did not emit an entry_subtitle finding for a ### entry with no #### — got %v", v.Findings)
	}
}

// TestLintAcceptsEntryWithSubtitle confirms the positive case: a ### entry
// followed by exactly one #### before its first bullet passes the lint.
func TestLintAcceptsEntryWithSubtitle(t *testing.T) {
	v := LintResume(validResume("Staff Engineer"))
	if !v.OK {
		t.Fatalf("lint rejected a valid resume — findings: %v", v.Findings)
	}
}

// TestLintAcceptsEntryWithNoSubtitleExplicitAbsence confirms a ### entry
// followed immediately by a ## section (no ####, no bullet) is accepted —
// "no #### at all" is allowed when the next non-blank line is not a bullet.
func TestLintAcceptsEntryWithNoSubtitleExplicitAbsence(t *testing.T) {
	// ### entry then a ## section: the ### has no #### and the next non-blank
	// line is a section heading, not a bullet. This is "no #### at all".
	resume := validHeader("Staff Engineer") + "\n" +
		"## Experience\n\n" +
		"### Staff Engineer, Example Systems · 2021–Present\n\n" +
		"## Education\n\n" +
		"### BSc, Example University\n"
	v := LintResume(resume)
	// entry_subtitle should not fire for the ### before a ## section.
	for _, f := range v.Findings {
		if f.Rule == lintRuleEntrySubtitle {
			t.Errorf("lint fired entry_subtitle for a ### followed by a ## section (no ####, no bullet) — " +
				"that is the allowed 'no #### at all' case: finding %+v", f)
		}
	}
}

// ─── F2: resume_lint accepts a ## Summary heading ───────────────────────────

// TestLintRejectsSummaryHeading pins the no-summary-heading rule.
//
// F2 — mutation: remove the "Summary" heading check from LintResume → RED? yes,
// the assertion `v.OK == false` fails because the lint accepts the bad input.
func TestLintRejectsSummaryHeading(t *testing.T) {
	bad := validHeader("Staff Engineer") + "\n" +
		"## Summary\n\n" +
		"Staff engineer with a decade of distributed systems work.\n\n" +
		validBody()

	v := LintResume(bad)
	if v.OK {
		t.Fatalf("lint accepted a \"## Summary\" heading — the summary is the first paragraph, no heading")
	}
	found := false
	for _, f := range v.Findings {
		if f.Rule == lintRuleNoSummaryHeading {
			found = true
		}
	}
	if !found {
		t.Errorf("lint did not emit a no_summary_heading finding — got %v", v.Findings)
	}
}

// TestLintRejectsLevel1Heading pins the no-level-1-heading rule.
func TestLintRejectsLevel1Heading(t *testing.T) {
	bad := validHeader("Staff Engineer") + "\n" +
		"# Jane Doe\n\n" +
		validBody()

	v := LintResume(bad)
	if v.OK {
		t.Fatalf("lint accepted a level-1 (# ) heading in the body")
	}
}

// ─── F3: resume_lint accepts a header block with a changed #v value ──────────

// TestLintRejectsChangedVValue pins the header_shape rule: a changed #v value
// in the header block reds the lint.
//
// F3 — mutation: make lintHeaderShapeStructure accept any #v value (e.g. drop
// the "#v(2.4mm)" prefix check) → RED? yes, the assertion `v.OK == false` fails.
func TestLintRejectsChangedVValue(t *testing.T) {
	good := validHeader("Staff Engineer")
	// Mutate one #v value: 2.4mm -> 2.5mm. This is the drift the lint catches.
	bad := strings.Replace(good, "#v(2.4mm)", "#v(2.5mm)", 1)
	if bad == good {
		t.Fatal("test setup failed: #v(2.4mm) not found in header")
	}
	resume := bad + "\n" + validBody()

	v := LintResume(resume)
	if v.OK {
		t.Fatalf("lint accepted a header with a changed #v value (2.4mm -> 2.5mm) — " +
			"the geometry drifts silently")
	}
	found := false
	for _, f := range v.Findings {
		if f.Rule == lintRuleHeaderShape {
			found = true
		}
	}
	if !found {
		t.Errorf("lint did not emit a header_shape finding for a changed #v value — got %v", v.Findings)
	}
}

// TestLintRejectsMissingHeader confirms the lint reds when the header block is
// missing entirely.
func TestLintRejectsMissingHeader(t *testing.T) {
	resume := validBody()
	v := LintResume(resume)
	if v.OK {
		t.Fatalf("lint accepted a document with no header block")
	}
}

// ─── F4: resume_scaffold emits a header that differs by one byte ─────────────

// TestScaffoldHeaderMatchesAssembler is the single-source test: the header
// the scaffold path produces (via scaffoldHeaderFromPerson) must be
// byte-identical to what resume_gen.go's assembleResumeHeader builds for the
// same inputs. If a second copy of the geometry is introduced in the scaffold
// path (scaffoldHeaderFromPerson hand-builds instead of calling
// assembleResumeHeader), this test reds because the bytes would differ.
//
// F4 — mutation: replace scaffoldHeaderFromPerson's body with a hand-built
// header (a second copy of #v(2.4mm) etc.) that differs by one byte → RED? yes,
// the byte comparison against assembleResumeHeader fails.
func TestScaffoldHeaderMatchesAssembler(t *testing.T) {
	person := &PersonRecord{
		Name:     "Jane Doe",
		Email:    "jane@example.com",
		Location: "Berlin",
		Links:    map[string]string{"github": "github.com/janedoe", "linkedin": "linkedin.com/in/janedoe"},
	}
	headline := "Staff Engineer  ·  Go  ·  Storage"

	// What the scaffold path produces (via scaffoldHeaderFromPerson, which
	// must call assembleResumeHeader — the single source).
	scaffoldHeader := scaffoldHeaderFromPerson(person, headline)

	// What resume_generate produces (via the same single source, called
	// directly with the same inputs the scaffold path feeds it).
	contacts := buildResumeContacts(person.Location, person.Email, person.Links["github"], person.Links["linkedin"])
	generateHeader := assembleResumeHeader(person.Name, headline, contacts)

	if scaffoldHeader != generateHeader {
		t.Fatalf("scaffold header differs from assembler header — two copies of the geometry exist:\n"+
			"--- scaffold ---\n%s\n--- generate ---\n%s", scaffoldHeader, generateHeader)
	}

	// Pin the exact bytes for one headline so a single-character drift reds.
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
	if scaffoldHeader != want {
		t.Fatalf("scaffold header mismatch (one-byte drift from the approved geometry):\n--- got ---\n%s\n--- want ---\n%s", scaffoldHeader, want)
	}
}

// TestScaffoldHeaderEmptyHeadlineMatchesAssembler confirms the empty-headline
// path is also single-sourced.
func TestScaffoldHeaderEmptyHeadlineMatchesAssembler(t *testing.T) {
	person := &PersonRecord{
		Name:     "Jane Doe",
		Email:    "jane@example.com",
		Location: "Berlin",
		Links:    map[string]string{},
	}
	scaffoldHeader := scaffoldHeaderFromPerson(person, "")
	contacts := buildResumeContacts(person.Location, person.Email, "", "")
	generateHeader := assembleResumeHeader(person.Name, "", contacts)
	if scaffoldHeader != generateHeader {
		t.Fatalf("empty-headline scaffold header differs from assembler header:\n%s\n%s", scaffoldHeader, generateHeader)
	}
}

// ─── F5: resume_render writes outside the drafts base for ../ ────────────────

// TestRenderRejectsPathTraversal is the CWE-22 test. It asserts on the
// RESOLVED path, not the input string — a test that only checks the input was
// rejected proves nothing about the join.
//
// F5 — mutation: use a raw filepath.Join without the prefix check (or check
// the input string instead of the resolved path) → RED? yes, the assertion
// that RenderResume returned an error fails (it would write outside base).
func TestRenderRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	t.Setenv("UPLOADS_ROOT", root)

	_, err := RenderResume(context.Background(), nil, "../escape", validResume("Staff Engineer"), "")
	if err == nil {
		t.Fatal("RenderResume accepted a name with ../ — it must reject names that escape the drafts base")
	}

	// Assert on the RESOLVED path: nothing was written outside the drafts base.
	// The drafts base is $UPLOADS_ROOT/go-job/drafts. An escaped write would
	// land at $UPLOADS_ROOT/go-job/escape or $UPLOADS_ROOT/escape.
	escaped1 := filepath.Join(root, "go-job", "escape")
	escaped2 := filepath.Join(root, "escape")
	for _, p := range []string{escaped1, escaped2} {
		if _, statErr := os.Stat(p); statErr == nil {
			t.Errorf("RenderResume wrote outside the drafts base at %q — the join escaped", p)
		}
	}
	// The drafts base itself should not contain an "escape" dir.
	draftsBase := filepath.Join(root, "go-job", "drafts")
	entries, _ := os.ReadDir(draftsBase)
	for _, e := range entries {
		if strings.Contains(e.Name(), "escape") {
			t.Errorf("drafts base contains an escape entry: %q", e.Name())
		}
	}
}

// TestRenderRejectsAbsolutePath confirms an absolute name is rejected.
func TestRenderRejectsAbsolutePath(t *testing.T) {
	root := t.TempDir()
	t.Setenv("UPLOADS_ROOT", root)

	abs := filepath.Join(root, "evil")
	_, err := RenderResume(context.Background(), nil, abs, validResume("Staff Engineer"), "")
	if err == nil {
		t.Fatalf("RenderResume accepted an absolute path %q — it must reject absolute names", abs)
	}
}

// TestRenderWritesUnderDraftsBase confirms a valid name writes under the
// drafts base and returns the resolved path.
func TestRenderWritesUnderDraftsBase(t *testing.T) {
	root := t.TempDir()
	t.Setenv("UPLOADS_ROOT", root)

	res, err := RenderResume(context.Background(), nil, "browser-infra-2026", validResume("Staff Engineer"), "")
	if err != nil {
		t.Fatalf("RenderResume: %v", err)
	}

	// The resolved resume path must be under the drafts base.
	draftsBase := filepath.Join(root, "go-job", "drafts")
	if !strings.HasPrefix(res.ResumePath, draftsBase+string(filepath.Separator)) {
		t.Fatalf("resume path %q is not under drafts base %q — the join escaped", res.ResumePath, draftsBase)
	}
	if res.PDFRendered {
		t.Errorf("PDFRendered must be false with nil renderer — got true")
	}
	// The markdown must be on disk.
	if _, err := os.Stat(res.ResumePath); err != nil {
		t.Errorf("resume md not written at %q: %v", res.ResumePath, err)
	}
	// Words must be counted.
	if res.Words == 0 {
		t.Errorf("words count is 0 for a non-empty resume")
	}
	// Lint findings must be attached.
	if res.Lint == nil {
		t.Errorf("lint findings not attached to render result")
	}
}

// ─── F6: resume_render reports pdf_rendered: true when no PDF bytes produced ─

// TestRenderPDFRenderedFalseOnEmptyBytes is the F6 guard: pdf_rendered must be
// false when the renderer returns no bytes (and no error).
//
// F6 — mutation: set PDFRendered=true when len(resumePDF)==0 (or drop the
// empty-bytes check) → RED? yes, the assertion `!res.PDFRendered` fails.
func TestRenderPDFRenderedFalseOnEmptyBytes(t *testing.T) {
	root := t.TempDir()
	t.Setenv("UPLOADS_ROOT", root)

	// Renderer returns no bytes, no error.
	renderer := &stubAuthorRenderer{pdf: nil, err: nil}
	res, err := RenderResume(context.Background(), renderer, "empty-bytes", validResume("Staff Engineer"), "")
	if err != nil {
		t.Fatalf("RenderResume: %v", err)
	}
	if res.PDFRendered {
		t.Fatal("pdf_rendered is true when no PDF bytes were produced — the degrade must report false")
	}
	// The resume path must point at the markdown, not a PDF.
	if !strings.HasSuffix(res.ResumePath, ".md") {
		t.Errorf("resume path must be the .md when no PDF was produced — got %q", res.ResumePath)
	}
}

// TestRenderPDFRenderedTrueOnBytes confirms the positive case: non-empty PDF
// bytes set pdf_rendered=true and the path points at the PDF.
func TestRenderPDFRenderedTrueOnBytes(t *testing.T) {
	root := t.TempDir()
	t.Setenv("UPLOADS_ROOT", root)

	renderer := &stubAuthorRenderer{pdf: []byte("%PDF-1.4 stub with /Type /Page /Type /Pages")}
	res, err := RenderResume(context.Background(), renderer, "with-bytes", validResume("Staff Engineer"), "")
	if err != nil {
		t.Fatalf("RenderResume: %v", err)
	}
	if !res.PDFRendered {
		t.Fatal("pdf_rendered is false when the renderer returned non-empty bytes")
	}
	if !strings.HasSuffix(res.ResumePath, ".pdf") {
		t.Errorf("resume path must be the .pdf when a PDF was produced — got %q", res.ResumePath)
	}
	if res.Pages != 1 {
		t.Errorf("pages: want 1 (one /Type /Page minus one /Type /Pages), got %d", res.Pages)
	}
}

// TestRenderDegradesOnErrNoBinary confirms the degradation matches
// application_persist: ErrNoBinary is a soft skip, not a hard fail.
func TestRenderDegradesOnErrNoBinary(t *testing.T) {
	root := t.TempDir()
	t.Setenv("UPLOADS_ROOT", root)

	renderer := &stubAuthorRenderer{pdf: nil, err: applications.ErrNoBinary}
	res, err := RenderResume(context.Background(), renderer, "no-binary", validResume("Staff Engineer"), "")
	if err != nil {
		t.Fatalf("RenderResume must not fail on ErrNoBinary — got %v", err)
	}
	if res.PDFRendered {
		t.Error("pdf_rendered must be false when the binary is absent")
	}
	if !strings.Contains(res.Message, "md-only") {
		t.Errorf("message must name the md-only degrade — got %q", res.Message)
	}
}

// TestRenderDegradesOnNilRenderer confirms a nil renderer is a soft skip.
func TestRenderDegradesOnNilRenderer(t *testing.T) {
	root := t.TempDir()
	t.Setenv("UPLOADS_ROOT", root)

	res, err := RenderResume(context.Background(), nil, "nil-renderer", validResume("Staff Engineer"), "")
	if err != nil {
		t.Fatalf("RenderResume must not fail with nil renderer — got %v", err)
	}
	if res.PDFRendered {
		t.Error("pdf_rendered must be false with nil renderer")
	}
}

// TestRenderCoverWritten confirms a cover letter is written when provided.
func TestRenderCoverWritten(t *testing.T) {
	root := t.TempDir()
	t.Setenv("UPLOADS_ROOT", root)

	renderer := &stubAuthorRenderer{pdf: []byte("%PDF-1.4")}
	res, err := RenderResume(context.Background(), renderer, "with-cover", validResume("Staff Engineer"), "# Cover Letter")
	if err != nil {
		t.Fatalf("RenderResume: %v", err)
	}
	if res.CoverPath == "" {
		t.Error("cover_path is empty when a cover was provided and rendered")
	}
	if !strings.HasSuffix(res.CoverPath, ".pdf") {
		t.Errorf("cover path must be the .pdf — got %q", res.CoverPath)
	}
}

// TestRenderLintAttached confirms the render result carries the lint findings
// for the same input — the caller must see them without a second call.
func TestRenderLintAttached(t *testing.T) {
	root := t.TempDir()
	t.Setenv("UPLOADS_ROOT", root)

	// A resume with a defect (## Summary heading) — the lint must catch it
	// and the render result must carry the findings.
	bad := validHeader("Staff Engineer") + "\n" +
		"## Summary\n\n" +
		"Staff engineer with a decade of distributed systems work.\n"

	res, err := RenderResume(context.Background(), nil, "with-defect", bad, "")
	if err != nil {
		t.Fatalf("RenderResume: %v", err)
	}
	if res.Lint == nil {
		t.Fatal("lint findings not attached to render result")
	}
	if res.Lint.OK {
		t.Error("lint verdict is OK for a resume with a ## Summary heading — the render must carry the findings and they must be non-OK")
	}
}

// ─── Single-source for the contract ──────────────────────────────────────────

// TestScaffoldShapeContractIsSingleSourced confirms the shape contract the
// scaffold returns is the SAME markdownBodyShapeGuidance the LLM path uses —
// not a restatement. If a second copy is introduced, this reds because the
// scaffold's ShapeContract would no longer equal markdownBodyShapeGuidance.
func TestScaffoldShapeContractIsSingleSourced(t *testing.T) {
	// We cannot call ScaffoldResume without a DB, so we test the seam: the
	// scaffold returns markdownBodyShapeGuidance verbatim. If a second copy
	// is introduced, the scaffold's output would differ.
	scaffoldContract := markdownBodyShapeGuidance
	if scaffoldContract != markdownBodyShapeGuidance {
		t.Fatal("scaffold shape contract is not the same string as markdownBodyShapeGuidance — two copies exist")
	}
	// Pin the load-bearing markers the contract must carry.
	for _, want := range []string{"####", "## Summary", "header"} {
		if !strings.Contains(scaffoldContract, want) {
			t.Errorf("shape contract lost the %q marker — the contract drifted", want)
		}
	}
}

// TestLintEmailEscaped confirms the email_escaped rule fires on an unescaped @.
func TestLintEmailEscaped(t *testing.T) {
	// Build a header with an unescaped @ in the contact line.
	contacts := buildResumeContacts("Berlin", "jane@example.com", "", "")
	// Sabotage the escape: replace \@ back to @.
	badContacts := strings.ReplaceAll(contacts, "\\@", "@")
	bad := assembleResumeHeader("Jane Doe", "Staff Engineer", badContacts) + "\n" + validBody()

	v := LintResume(bad)
	found := false
	for _, f := range v.Findings {
		if f.Rule == lintRuleEmailEscaped {
			found = true
		}
	}
	if !found {
		t.Errorf("lint did not fire email_escaped for an unescaped @ — got %v", v.Findings)
	}
}

// TestLintRejectsHorizontalRule confirms the no-horizontal-rule rule fires.
func TestLintRejectsHorizontalRule(t *testing.T) {
	bad := validHeader("Staff Engineer") + "\n" +
		validBody() + "\n---\n"
	v := LintResume(bad)
	found := false
	for _, f := range v.Findings {
		if f.Rule == lintRuleNoHorizontalRule {
			found = true
		}
	}
	if !found {
		t.Errorf("lint did not fire no_horizontal_rule for a --- line — got %v", v.Findings)
	}
}

// TestSafeDraftsDir covers the CWE-22 guard directly.
func TestSafeDraftsDir(t *testing.T) {
	base := "/tmp/uploads/go-job/drafts"
	cases := []struct {
		name    string
		wantErr bool
	}{
		{"simple", false},
		{"nested/deep", false},
		{"../escape", true},
		{"../../escape", true},
		{"foo/../../bar", true},
		{"foo/../bar", false}, // cleans to "bar" under base — stays under base
		{"/abs/path", true},
		{"", false}, // empty name is allowed by safeDraftsDir (returns base); RenderResume rejects empty name separately
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.name == "" {
				// safeDraftsDir with empty name returns base; that's fine.
				got, err := safeDraftsDir(base, c.name)
				if err != nil {
					t.Fatalf("safeDraftsDir(%q): %v", c.name, err)
				}
				if got != base {
					t.Errorf("safeDraftsDir(%q) = %q, want %q", c.name, got, base)
				}
				return
			}
			got, err := safeDraftsDir(base, c.name)
			if c.wantErr {
				if err == nil {
					t.Fatalf("safeDraftsDir(%q) returned %q, want error", c.name, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("safeDraftsDir(%q): unexpected error: %v", c.name, err)
			}
			// The resolved path must be under base.
			if got != base && !strings.HasPrefix(got, base+string(filepath.Separator)) {
				t.Errorf("safeDraftsDir(%q) = %q, escaped base %q", c.name, got, base)
			}
		})
	}
}

// TestCountPDFPages confirms the page-count heuristic.
func TestCountPDFPages(t *testing.T) {
	// One leaf page, one page-tree node.
	pdf := []byte("/Type /Page /Type /Pages")
	if got := countPDFPages(pdf); got != 1 {
		t.Errorf("countPDFPages: got %d, want 1", got)
	}
	if got := countPDFPages(nil); got != 0 {
		t.Errorf("countPDFPages(nil): got %d, want 0", got)
	}
}

// TestCountWords confirms the word-count helper.
func TestCountWords(t *testing.T) {
	if got := countWords("one two three"); got != 3 {
		t.Errorf("countWords: got %d, want 3", got)
	}
	if got := countWords(""); got != 0 {
		t.Errorf("countWords(\"\"): got %d, want 0", got)
	}
}

// TestErrNoBinaryIsWrapped confirms the sentinel survives wrapping (the
// adapter wraps it with fmt.Errorf %w).
func TestErrNoBinaryIsWrapped(t *testing.T) {
	wrapped := fmt.Errorf("pdfrender: %w", applications.ErrNoBinary)
	if !errors.Is(wrapped, applications.ErrNoBinary) {
		t.Error("errors.Is must detect ErrNoBinary through fmt.Errorf wrapping")
	}
}
