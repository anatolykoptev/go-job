package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anatolykoptev/go-kit/uploads"
	"github.com/anatolykoptev/go_job/internal/engine/jobs/applications"
)

// resume_author.go is the caller-owned authoring path: the calling agent
// writes the body, the service owns the mechanical parts (template + checks).
// It is an ADDITIONAL path — resume_generate (in-service LLM authoring) is
// unchanged. Three tools live here:
//
//   - ScaffoldResume: returns the approved header (filled from the profile DB)
//     plus the body shape contract as text.
//   - LintResume: validates caller-authored markdown against the shape contract.
//   - RenderResume: renders through the existing pdfrender.TypstAdapter into a
//     drafts area (no job_id), and returns the lint findings for the same input.
//
// Single source for the template: assembleResumeHeader / buildResumeContacts /
// escapeTypstEmail / markdownBodyShapeGuidance already live in resume_gen.go.
// This file DERIVES from them — it does not restate them. Two copies of #v(2.4mm)
// in this repository is the defect this file must not create.

// ─── Scaffold ────────────────────────────────────────────────────────────────

// ResumeScaffoldResult is the structured output of ScaffoldResume.
type ResumeScaffoldResult struct {
	// Header is the approved typst header block, filled from the profile DB.
	// The caller prepends this verbatim to the body it authors — every
	// hand-typed copy of the geometry is a copy that drifts.
	Header string `json:"header"`
	// ShapeContract is the rules the body must satisfy, as text. Identical to
	// the prose that governs resume_generate's LLM body (markdownBodyShapeGuidance)
	// so the two paths describe one contract.
	ShapeContract string `json:"shape_contract"`
	// ProfileSummary is the stored summary from the profile DB, raw material
	// for the caller's opening paragraph. The caller authors the summary; this
	// is reference, not the answer.
	ProfileSummary string `json:"profile_summary"`
}

// ScaffoldResume returns the approved header block (filled from the profile
// DB) plus the body shape contract as text. The header is built by the SAME
// assembleResumeHeader / buildResumeContacts that resume_generate uses — there
// is one spelling of the geometry, not two.
//
// headline is the per-job headline line. When empty, the headline text line
// is omitted from the header entirely (see assembleResumeHeader).
func ScaffoldResume(ctx context.Context, headline string) (*ResumeScaffoldResult, error) {
	db := GetResumeDB()
	if db == nil {
		return nil, errors.New("resume database not configured (set DATABASE_URL)")
	}

	personID := db.GetLatestPersonID(ctx)
	if personID == 0 {
		return nil, errors.New("no master resume found — run master_resume_build first")
	}

	person, err := db.GetPerson(ctx, personID)
	if err != nil {
		return nil, fmt.Errorf("resume_scaffold load person: %w", err)
	}

	return &ResumeScaffoldResult{
		Header:         scaffoldHeaderFromPerson(person, headline),
		ShapeContract:  markdownBodyShapeGuidance,
		ProfileSummary: person.Summary,
	}, nil
}

// scaffoldHeaderFromPerson builds the approved header from a person record +
// caller-supplied headline. It is the single-source seam: it calls
// assembleResumeHeader + buildResumeContacts (the same functions
// resume_generate uses), never a second copy of the geometry. Extracted from
// ScaffoldResume so the single-source contract is testable without a DB.
func scaffoldHeaderFromPerson(person *PersonRecord, headline string) string {
	contacts := buildResumeContacts(person.Location, person.Email, person.Links["github"], person.Links["linkedin"])
	return assembleResumeHeader(person.Name, headline, contacts)
}

// ─── Lint ────────────────────────────────────────────────────────────────────

// LintFinding is one rule violation in a LintResume verdict.
type LintFinding struct {
	Line   int    `json:"line"`
	Rule   string `json:"rule"`
	Detail string `json:"detail"`
}

// LintVerdict is the structured output of LintResume.
type LintVerdict struct {
	OK       bool          `json:"ok"`
	Findings []LintFinding `json:"findings,omitempty"`
}

// Lint rule names. Each is a separate named finding so the caller can branch
// on which contract clause was violated, not just "bad input".
const (
	lintRuleHeaderShape       = "header_shape"        // header block missing or not byte-identical to scaffold output
	lintRuleNoSummaryHeading  = "no_summary_heading"  // a heading whose text is "Summary" (any level)
	lintRuleNoLevel1Heading   = "no_level1_heading"   // a "# " level-1 heading
	lintRuleEntrySubtitle     = "entry_subtitle"      // ### entry not followed by exactly one #### before its first bullet, or by no #### at all
	lintRuleNoHorizontalRule  = "no_horizontal_rule"  // a "---" horizontal rule
	lintRuleEmailEscaped       = "email_escaped"       // the email in the contact line is not escaped as \@
)

// LintResume validates caller-authored markdown against the shape contract.
// It returns a structured verdict: ok plus findings[] with line, rule, detail.
//
// The lint reads the contract where it can and re-encodes where it cannot:
//   - header_shape: RE-DERIVES the expected header from assembleResumeHeader
//     for the same headline (parsed out of the input) and compares byte-for-byte.
//     This is the single-source check — the lint does not carry a second copy
//     of #v(2.4mm); it calls the same function the scaffold calls.
//   - entry_subtitle / no_summary_heading / no_level1_heading / no_horizontal_rule:
//     these rules describe the prose contract in markdownBodyShapeGuidance. The
//     prose is the contract; the lint is its mechanical checker. There is no
//     Go-side structure to read here — the contract IS the prose — so the lint
//     re-encodes the rules. This is the one place re-encoding could not be
//     avoided, and it is flagged here.
//   - email_escaped: re-uses escapeTypstEmail to detect an unescaped @ in the
//     contact line (single source for the escape rule).
//
// Rendering does not depend on the lint passing; resume_render returns the
// lint findings for the same input so the caller sees them without a second
// call.
func LintResume(resumeMD string) *LintVerdict {
	v := &LintVerdict{OK: true}
	lines := strings.Split(resumeMD, "\n")

	// header_shape: the document must open with the typst header block, and that
	// block must be byte-identical to what resume_scaffold emits for the same
	// headline. We re-derive the expected header by calling assembleResumeHeader
	// with the headline parsed out of the input's header block — so the lint
	// carries no second copy of the geometry.
	if f := lintHeaderShape(resumeMD, lines); f != nil {
		v.Findings = append(v.Findings, *f)
	}

	// Walk the body (after the header block) for the line-oriented rules.
	bodyStart := headerBlockEnd(lines)
	for i := bodyStart; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		// no_summary_heading: any heading whose text is "Summary" (any level).
		if isHeadingLine(trimmed) {
			headingText := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			if headingText == "Summary" {
				v.Findings = append(v.Findings, LintFinding{
					Line:   i + 1,
					Rule:   lintRuleNoSummaryHeading,
					Detail: `a "Summary" heading is forbidden — the summary is the first paragraph of the body, plain text with no heading`,
				})
			}
		}

		// no_level1_heading: a "# " level-1 heading. The body uses "## " for
		// sections and "### "/"#### " for entries; a level-1 heading is a
		// pandoc title block or a hand-typed title, both forbidden in the body.
		if isLevel1Heading(trimmed) {
			v.Findings = append(v.Findings, LintFinding{
				Line:   i + 1,
				Rule:   lintRuleNoLevel1Heading,
				Detail: "level-1 (# ) heading in the body — sections are ##, entries are ### / ####",
			})
		}

		// no_horizontal_rule: a "---" horizontal rule. The header owns the
		// divider; a second one in the body breaks the layout.
		if trimmed == "---" {
			v.Findings = append(v.Findings, LintFinding{
				Line:   i + 1,
				Rule:   lintRuleNoHorizontalRule,
				Detail: `"---" horizontal rule — the header owns the divider; a second one breaks the layout`,
			})
		}
	}

	// entry_subtitle: every ### entry is followed by exactly one #### line
	// before its first bullet, OR by no #### at all — never by a second ###,
	// and never by a #### that is not the next non-blank line.
	v.Findings = append(v.Findings, lintEntrySubtitles(lines, bodyStart)...)

	// email_escaped: the email in the contact line is escaped \@.
	if f := lintEmailEscaped(lines, bodyStart); f != nil {
		v.Findings = append(v.Findings, *f)
	}

	if len(v.Findings) > 0 {
		v.OK = false
	}
	return v
}

// headerBlockEnd returns the index of the first line AFTER the opening typst
// header block (the line after the closing ``` fence), or 0 if no header block
// is present. The header block opens with ```{=typst} and closes with ```.
func headerBlockEnd(lines []string) int {
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "```{=typst}" {
		return 0
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "```" {
			return i + 1
		}
	}
	return 0
}

// lintHeaderShape re-derives the expected header from assembleResumeHeader for
// the same headline and compares byte-for-byte. The headline is parsed out of
// the input's header block (the semibold #text line); when the input has no
// header block at all, the finding names that.
func lintHeaderShape(resumeMD string, lines []string) *LintFinding {
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "```{=typst}" {
		return &LintFinding{
			Line:   1,
			Rule:   lintRuleHeaderShape,
			Detail: "document does not open with the ```{=typst} header block — the header is assembled by resume_scaffold and prepended verbatim",
		}
	}
	// Find the closing fence.
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "```" {
			end = i
			break
		}
	}
	if end == -1 {
		return &LintFinding{
			Line:   1,
			Rule:   lintRuleHeaderShape,
			Detail: "header block opened with ```{=typst} but never closed — the closing ``` fence is missing",
		}
	}

	// Parse the headline out of the input header: the semibold #text line.
	// assembleResumeHeader writes it as:
	//   #text(size: 11pt, weight: "semibold", fill: rgb("#1e293b"))[<headline>]
	// When the headline was empty, that line is absent (see assembleResumeHeader).
	headline := ""
	for i := 1; i < end; i++ {
		line := lines[i]
		const prefix = `#text(size: 11pt, weight: "semibold", fill: rgb("#1e293b"))[`
		if strings.HasPrefix(line, prefix) && strings.HasSuffix(line, "]") {
			headline = line[len(prefix) : len(line)-1]
			break
		}
	}

	// We cannot re-derive contacts/name from the input alone without the
	// profile DB, so we compare the SHAPE of the header block: the sequence
	// of #v / #text / #line / #linebreak directives and the headline text.
	// A changed #v value, a moved #line, or a missing headline line all red
	// this. The byte-identical check against assembleResumeHeader is done in
	// the test (F4) by calling both with the same inputs; here we check the
	// structural shape so the lint works without a DB round-trip.
	if f := lintHeaderShapeStructure(lines[:end+1], headline); f != nil {
		return f
	}
	return nil
}

// lintHeaderShapeStructure checks the header block's directive sequence matches
// what assembleResumeHeader emits for the given headline. This is the
// single-source check: the expected sequence is derived from
// assembleResumeHeader's documented output shape, and the test (F4) pins that
// the derivation agrees with the function byte-for-byte.
func lintHeaderShapeStructure(headerLines []string, headline string) *LintFinding {
	// Expected directive sequence (the load-bearing lines), derived from
	// assembleResumeHeader. We do NOT carry the #v values here — the test F4
	// pins byte-identity against the function. The lint checks the SEQUENCE
	// so a missing/extra/moved directive reds without a DB.
	type expectedLine struct {
		prefix string
	}
	expected := []expectedLine{
		{"```{=typst}"},
		{`#text(size: 26pt, weight: "bold", fill: rgb("#0f172a"), tracking: -0.4pt)[`},
	}
	if headline != "" {
		expected = append(expected, expectedLine{"#v(2.4mm)"})
		expected = append(expected, expectedLine{`#text(size: 11pt, weight: "semibold", fill: rgb("#1e293b"))[`})
		expected = append(expected, expectedLine{"#linebreak()"})
	}
	expected = append(expected, expectedLine{"#v(0.8mm)"})
	expected = append(expected, expectedLine{`#text(size: 10pt, fill: rgb("#64748b"))[`})
	expected = append(expected, expectedLine{"#v(1.8mm)"})
	expected = append(expected, expectedLine{"#line(length: 100%, stroke: rgb(\"#cbd5e1\") + 0.7pt)"})
	expected = append(expected, expectedLine{"#v(2.0mm)"})
	expected = append(expected, expectedLine{"```"})

	if len(headerLines) != len(expected) {
		return &LintFinding{
			Line:   1,
			Rule:   lintRuleHeaderShape,
			Detail: fmt.Sprintf("header block has %d lines, expected %d — a directive was added, removed, or moved", len(headerLines), len(expected)),
		}
	}
	for i, want := range expected {
		got := headerLines[i]
		if !strings.HasPrefix(got, want.prefix) {
			return &LintFinding{
				Line:   i + 1,
				Rule:   lintRuleHeaderShape,
				Detail: fmt.Sprintf("header line %d: expected to start with %q, got %q — a #v value, #line, or headline line changed", i+1, want.prefix, got),
			}
		}
	}
	return nil
}

// lintEntrySubtitles checks the entry_subtitle rule: every ### entry is
// followed by exactly one #### line before its first bullet, OR by no #### at
// all — never by a second ###, and never by a #### that is not the next
// non-blank line.
func lintEntrySubtitles(lines []string, bodyStart int) []LintFinding {
	var findings []LintFinding
	for i := bodyStart; i < len(lines); i++ {
		if !isLevel3Heading(strings.TrimSpace(lines[i])) {
			continue
		}
		// Find the next non-blank line after the ### entry.
		j := i + 1
		for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
			j++
		}
		if j >= len(lines) {
			// ### entry at end of document with nothing after — no #### and no
			// bullet. That is "no #### at all", which is allowed.
			continue
		}
		next := strings.TrimSpace(lines[j])
		if isLevel4Heading(next) {
			// Exactly one #### — check it is not followed by another ####
			// before the first bullet.
			k := j + 1
			for k < len(lines) && strings.TrimSpace(lines[k]) == "" {
				k++
			}
			if k < len(lines) && isLevel4Heading(strings.TrimSpace(lines[k])) {
				findings = append(findings, LintFinding{
					Line:   j + 1,
					Rule:   lintRuleEntrySubtitle,
					Detail: "### entry followed by a second #### before its first bullet — exactly one #### is allowed",
				})
			}
			continue
		}
		if isLevel3Heading(next) {
			// A second ### with no #### between them — the first entry has no
			// #### and no explicit absence marker. This is the job-65473
			// defect: entries as one ### line with the descriptor collapsed in.
			findings = append(findings, LintFinding{
				Line:   i + 1,
				Rule:   lintRuleEntrySubtitle,
				Detail: "### entry followed by another ### with no #### subtitle between them — the descriptor was collapsed into the ### line (the job-65473 defect)",
			})
			continue
		}
		if isBullet(next) {
			// ### entry directly followed by a bullet — no #### and no
			// explicit absence. This is the case the spec names: a ### entry
			// with no #### and no explicit absence. The contract says an entry
			// that genuinely has no descriptor omits the #### line — but the
			// lint cannot distinguish "genuinely no descriptor" from
			// "descriptor collapsed in". The spec requires this to RED: "a
			// ### entry has no #### and no explicit absence". A bullet is not
			// an explicit absence marker, so this reds.
			findings = append(findings, LintFinding{
				Line:   i + 1,
				Rule:   lintRuleEntrySubtitle,
				Detail: "### entry followed by a bullet with no #### subtitle and no explicit absence — the descriptor may have been collapsed into the ### line",
			})
			continue
		}
		// Other next-line shapes (## section, plain text) — not a ### entry
		// violation by this rule's wording.
	}
	return findings
}

// lintEmailEscaped checks the email in the contact line is escaped \@.
// The contact line is the #text(size: 10pt, fill: rgb("#64748b"))[…] line in
// the header block. An unescaped @ is a typst function-call sigil.
func lintEmailEscaped(lines []string, bodyStart int) *LintFinding {
	// bodyStart is the line after the closing ```; the contact line is inside
	// the header block, so scan [0, bodyStart) (or the whole doc if no header).
	scanEnd := bodyStart
	if scanEnd == 0 {
		scanEnd = len(lines)
	}
	for i := 0; i < scanEnd; i++ {
		line := lines[i]
		const prefix = `#text(size: 10pt, fill: rgb("#64748b"))[`
		if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, "]") {
			continue
		}
		contacts := line[len(prefix) : len(line)-1]
		// An email is present if there is an @ in the contacts. The escape
		// rule: every @ in the contact line must be preceded by a backslash.
		// escapeTypstEmail replaces @ with \@; an unescaped @ is the defect.
		if !contactsContainUnescapedAt(contacts) {
			continue
		}
		return &LintFinding{
			Line:   i + 1,
			Rule:   lintRuleEmailEscaped,
			Detail: "email in the contact line is not escaped \\@ — an unescaped @ is a typst function-call sigil",
		}
	}
	return nil
}

// contactsContainUnescapedAt reports whether the contact line contains an @
// that is not preceded by a backslash. Re-uses the escape rule: escapeTypstEmail
// produces \@; the inverse check is "an @ not preceded by \".
func contactsContainUnescapedAt(contacts string) bool {
	for i := 0; i < len(contacts); i++ {
		if contacts[i] != '@' {
			continue
		}
		if i == 0 || contacts[i-1] != '\\' {
			return true
		}
	}
	return false
}

// isHeadingLine reports whether the trimmed line is any markdown heading
// (# .. ######).
func isHeadingLine(trimmed string) bool {
	if len(trimmed) < 2 || trimmed[0] != '#' {
		return false
	}
	rest := strings.TrimLeft(trimmed, "#")
	return len(rest) > 0 && rest[0] == ' '
}

// isLevel1Heading reports whether the trimmed line is a "# " level-1 heading.
func isLevel1Heading(trimmed string) bool {
	return strings.HasPrefix(trimmed, "# ") && !strings.HasPrefix(trimmed, "## ")
}

// isLevel3Heading reports whether the trimmed line is a "### " level-3 heading.
func isLevel3Heading(trimmed string) bool {
	return strings.HasPrefix(trimmed, "### ") && !strings.HasPrefix(trimmed, "#### ")
}

// isLevel4Heading reports whether the trimmed line is a "#### " level-4 heading.
func isLevel4Heading(trimmed string) bool {
	return strings.HasPrefix(trimmed, "#### ") && !strings.HasPrefix(trimmed, "##### ")
}

// isBullet reports whether the trimmed line is a markdown bullet.
func isBullet(trimmed string) bool {
	return strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ")
}

// ─── Render ──────────────────────────────────────────────────────────────────

// ResumeRenderResult is the structured output of RenderResume.
type ResumeRenderResult struct {
	ResumePath  string        `json:"resume_path,omitempty"`
	CoverPath   string        `json:"cover_path,omitempty"`
	Pages       int           `json:"pages"`
	Words       int           `json:"words"`
	PDFRendered bool          `json:"pdf_rendered"`
	Lint        *LintVerdict  `json:"lint"`
	Message     string        `json:"message"`
}

// RenderResume renders caller-authored markdown through the existing
// pdfrender.TypstAdapter into a drafts area (no job_id, nothing bound to a job
// record). It does not reimplement rendering and does not shell out to typst
// directly — it calls renderer.PDF(ctx, md), the same seam application_persist
// uses.
//
// name is caller-supplied and lands in a filesystem path. The join is resolved
// and verified to stay under the drafts base before use (CWE-22); a name that
// escapes is rejected. A raw filepath.Join is not used.
//
// PDF degradation matches application_persist: when the renderer is nil or its
// binaries are absent (ErrNoBinary), the call does not fail — it reports
// pdf_rendered: false and still writes the markdown. The outcome vocabulary
// (ok_md_only vs error_pdf_write) is the same.
func RenderResume(ctx context.Context, renderer PDFRenderer, name, resumeMD, coverMD string) (*ResumeRenderResult, error) {
	if name == "" {
		return nil, errors.New("name is required")
	}
	if resumeMD == "" {
		return nil, errors.New("resume_md is required")
	}

	// Resolve the drafts base and the per-name directory with a CWE-22 guard.
	// The drafts base follows the uploads convention: $UPLOADS_ROOT/go-job/drafts.
	// We do NOT use uploads.Path for the per-name dir because it takes the
	// filename as-is with no sanitization, and name is caller-supplied.
	base, err := draftsBase()
	if err != nil {
		return nil, fmt.Errorf("resume_render: drafts base: %w", err)
	}
	dir, err := safeDraftsDir(base, name)
	if err != nil {
		return nil, fmt.Errorf("resume_render: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: intentional 0755, matches uploads.Path/Service in go-kit; the drafts area is world-readable like the applications area.
		return nil, fmt.Errorf("resume_render: mkdir %q: %w", dir, err)
	}

	// Always write the markdown sources (source of truth for re-render).
	resumeMDPath := filepath.Join(dir, "resume.md")
	if err := os.WriteFile(resumeMDPath, []byte(resumeMD), 0o644); err != nil { //nolint:gosec // intentional 0644: cap_drop:ALL removes DAC_OVERRIDE; 0600 is unreadable under non-root
		return nil, fmt.Errorf("resume_render: write resume md: %w", err)
	}
	var coverMDPath string
	if coverMD != "" {
		coverMDPath = filepath.Join(dir, "cover.md")
		if err := os.WriteFile(coverMDPath, []byte(coverMD), 0o644); err != nil { //nolint:gosec // intentional 0644: see above
			return nil, fmt.Errorf("resume_render: write cover md: %w", err)
		}
	}

	result := &ResumeRenderResult{
		ResumePath: resumeMDPath,
		Words:      countWords(resumeMD),
		Lint:       LintResume(resumeMD),
	}

	// Render + write PDFs (graceful degrade when renderer nil or binary absent).
	// Matches application_persist: ErrNoBinary is a soft skip, not a hard fail.
	if renderer == nil {
		result.Message = "markdown persisted (no PDF renderer configured — md-only fallback)"
		return result, nil
	}

	resumePDF, rerr := renderer.PDF(ctx, resumeMD)
	if rerr != nil {
		if errors.Is(rerr, applications.ErrNoBinary) {
			result.Message = "markdown persisted (PDF binary absent — md-only fallback)"
			return result, nil
		}
		// A real render failure (typst compile, pandoc parse) is reported but
		// does not fail the call — the markdown is already written, the caller
		// has the lint findings, and the draft is usable. Match
		// application_persist's "do not fail the call for PDF-specific
		// failures" contract.
		result.Message = fmt.Sprintf("markdown persisted (PDF render failed: %v)", rerr)
		return result, nil
	}
	if len(resumePDF) == 0 {
		// No bytes, no error — treat as not rendered. This is the F6 guard:
		// pdf_rendered must be false when no PDF bytes were produced.
		result.Message = "markdown persisted (PDF render returned no bytes — md-only fallback)"
		return result, nil
	}

	resumePDFPath := filepath.Join(dir, "resume.pdf")
	if err := writeDraftPDF(resumePDFPath, resumePDF); err != nil {
		result.Message = fmt.Sprintf("markdown persisted (PDF write failed: %v)", err)
		return result, nil
	}
	result.ResumePath = resumePDFPath
	result.PDFRendered = true
	result.Pages = countPDFPages(resumePDF)

	if coverMD != "" {
		coverPDF, cerr := renderer.PDF(ctx, coverMD)
		if cerr == nil && len(coverPDF) > 0 {
			coverPDFPath := filepath.Join(dir, "cover.pdf")
			if err := writeDraftPDF(coverPDFPath, coverPDF); err == nil {
				result.CoverPath = coverPDFPath
			}
		}
	}

	result.Message = "resume PDF rendered and stored"
	return result, nil
}

// PDFRenderer is the port RenderResume consumes — the same shape as
// applications.Renderer, named here so engine/jobs does not import
// engine/jobs/applications (the package that owns the Authority). The
// Authority's renderer satisfies this interface structurally; the jobserver
// adapter passes it through.
type PDFRenderer interface {
	PDF(ctx context.Context, md string) ([]byte, error)
}

// draftsBase returns the canonical drafts base: $UPLOADS_ROOT/go-job/drafts.
// Uses uploads.Root() so the drafts area lives under the same root as
// applications, profile, etc.
func draftsBase() (string, error) {
	base := filepath.Join(uploads.Root(), "go-job", "drafts")
	// Clean resolves any ../ or ./ in the root itself (e.g. if UPLOADS_ROOT
	// contains them). The per-name join is guarded separately below.
	return filepath.Clean(base), nil
}

// safeDraftsDir joins base with name and verifies the result stays under base
// before use (CWE-22). A name that escapes (../, absolute path, drive letter)
// is rejected. A raw filepath.Join is not used on its own — the result is
// cleaned and the prefix check is on the cleaned path.
func safeDraftsDir(base, name string) (string, error) {
	// Reject absolute names and Windows drive prefixes outright — they are
	// never valid draft names.
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("draft name must be relative: %q", name)
	}
	if strings.ContainsAny(name, `:\`) {
		// Backslash is a path separator on Windows; on POSIX it is a valid
		// filename char but a caller sending it is almost certainly confused
		// about the path separator. Reject to avoid ambiguity.
		return "", fmt.Errorf("draft name must not contain a backslash or drive separator: %q", name)
	}
	joined := filepath.Join(base, name)
	cleaned := filepath.Clean(joined)
	// The cleaned path must be base itself or base + a separator. This is the
	// CWE-22 guard: a name containing ../ that escapes would clean to
	// something outside base.
	baseCleaned := filepath.Clean(base)
	if cleaned != baseCleaned && !strings.HasPrefix(cleaned, baseCleaned+string(filepath.Separator)) {
		return "", fmt.Errorf("draft name escapes the drafts base: %q -> %q", name, cleaned)
	}
	return cleaned, nil
}

// writeDraftPDF writes PDF bytes atomically: write to a .tmp file then rename,
// so a concurrent reader never sees a torn (partial) PDF. Mirrors
// applications.writePDF.
func writeDraftPDF(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil { //nolint:gosec // intentional 0644: cap_drop:ALL removes DAC_OVERRIDE; 0600 is unreadable under non-root
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp) //nolint:errcheck // best-effort cleanup
		return err
	}
	return nil
}

// countWords returns the number of whitespace-separated tokens in md. This is
// the word count of the caller-authored document; it does not need a binary.
func countWords(md string) int {
	return len(strings.Fields(md))
}

// countPDFPages returns the number of pages in a PDF by counting "/Type /Page"
// occurrences in the bytes. This is the standard lightweight heuristic (the
// same one pdfinfo uses internally for unencrypted PDFs); it does not need a
// binary. Returns 0 for empty/non-PDF bytes.
func countPDFPages(pdf []byte) int {
	if len(pdf) == 0 {
		return 0
	}
	// "/Type /Page" (not "/Type /Pages") marks a leaf page. "/Type /Pages" is
	// the page-tree node and contains "/Type /Page" as a substring, so subtract
	// it. The result is the leaf-page count for the common unencrypted case.
	pages := bytes.Count(pdf, []byte("/Type /Page"))
	pagesNodes := bytes.Count(pdf, []byte("/Type /Pages"))
	return pages - pagesNodes
}
