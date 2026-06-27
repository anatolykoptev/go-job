package typst

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/anatolykoptev/go-kit/render"
)

const (
	typstTimeout  = 45 * time.Second
	pandocTimeout = 15 * time.Second

	// resolveBinaryEnvTypst / resolveBinaryEnvPandoc are checked before PATH
	// for the typst/pandoc binaries. Set them in deployment to pin a binary
	// without modifying the system PATH.
	resolveBinaryEnvTypst  = "RENDER_TYPST_PATH"
	resolveBinaryEnvPandoc = "RENDER_PANDOC_PATH"

	// legacyEnvTypst / legacyEnvPandoc are the pre-v0.92.0 env var names
	// (from the vaelor era before this package was extracted into go-kit).
	// They are checked as a fallback when the new RENDER_* keys are unset,
	// with a one-time deprecation warning, so deployments that set VAELOR_*
	// continue to work until their env files are updated.
	legacyEnvTypst  = "VAELOR_TYPST_PATH"
	legacyEnvPandoc = "VAELOR_PANDOC_PATH"

	typstFormatPDF = "pdf"
	typstFormatPNG = "png"
)

// TypstRenderer converts markdown or HTML to a styled PDF via pandoc + typst.
// It replaces the Chrome CDP Printer for all text-document use-cases, producing
// true vector PDFs with real selectable text and file sizes ~10× smaller.
//
// Pipeline: content → pandoc (→ typst markup) → theme template → typst compile → PDF bytes
type TypstRenderer struct{}

// NewTypstRenderer creates a TypstRenderer. No state is held; safe for
// concurrent use.
func NewTypstRenderer() *TypstRenderer { return &TypstRenderer{} }

// typstDocData is the template context injected into a theme preamble.
type typstDocData struct {
	Title string
}

// pageSizeOverride returns a typst snippet that pins page width/height to
// the supplied pixel dimensions and margin. Returns "" when width/height
// are not both positive. The pt conversion uses the same PPI later passed
// to typst --ppi so on-page distances match exported pixels.
func pageSizeOverride(widthPx, heightPx, ppi int, marginPt float64) string {
	if widthPx <= 0 || heightPx <= 0 {
		return ""
	}
	if ppi <= 0 {
		ppi = 144
	}
	wPt := float64(widthPx) * 72.0 / float64(ppi)
	hPt := float64(heightPx) * 72.0 / float64(ppi)
	return fmt.Sprintf(
		"#set page(width: %spt, height: %spt, margin: %spt)\n",
		strconv.FormatFloat(wPt, 'f', 2, 64),
		strconv.FormatFloat(hPt, 'f', 2, 64),
		strconv.FormatFloat(marginPt, 'f', 2, 64),
	)
}

// Render converts content (markdown or HTML) to a PDF using the Typst toolchain.
//
// inputFmt must be "markdown" or "html" — passed directly to pandoc -f.
// opts.Theme controls the visual style (report/minimal/corporate).
// opts.Title sets the document title shown in headers.
func (r *TypstRenderer) Render(ctx context.Context, content, inputFmt string, opts render.Options) ([]byte, error) {
	if content == "" {
		return nil, errors.New("typst: content is empty")
	}

	doc, err := r.buildTypstSource(ctx, content, inputFmt, opts, "", false)
	if err != nil {
		return nil, err
	}
	return compileTypst(ctx, doc, typstOutput{Format: typstFormatPDF})
}

// RenderImage converts content to a PNG using the same Typst pipeline as
// Render. Unlike Render it honors opts.Width/opts.Height/opts.PPI so the
// output is a fixed-pixel raster suitable for OG cards, social posts and
// stories. Themes "card" and "dark" suppress the auto-injected H1 title
// block — they style the heading themselves.
func (r *TypstRenderer) RenderImage(ctx context.Context, content, inputFmt string, opts render.Options) ([]byte, error) {
	if content == "" {
		return nil, errors.New("typst: content is empty")
	}

	override := pageSizeOverride(opts.Width, opts.Height, opts.PPI, themePageMarginPt(opts.Theme))
	// TOC is forced off for image output — a table of contents inside a single-page raster is meaningless.
	imgOpts := opts
	imgOpts.TOC = false
	doc, err := r.buildTypstSource(ctx, content, inputFmt, imgOpts, override, themeOmitsTitleBlock(opts.Theme))
	if err != nil {
		return nil, err
	}
	return compileTypst(ctx, doc, typstOutput{Format: typstFormatPNG, PPI: opts.PPI})
}

// buildTypstSource produces the .typ source string consumed by compileTypst.
// override is the caller-supplied geometry block (empty string when none);
// omitTitle suppresses the auto-generated title block (themes that own
// their own title presentation use this).
func (r *TypstRenderer) buildTypstSource(
	ctx context.Context,
	content, inputFmt string,
	opts render.Options,
	override string,
	omitTitle bool,
) (string, error) {
	body, err := pandocConvert(ctx, content, inputFmt, opts.TOC)
	if err != nil {
		return "", fmt.Errorf("typst: pandoc %s→typst: %w", inputFmt, err)
	}

	theme := resolveTypstTheme(opts.Theme)
	preambleTmpl, err := template.New("preamble").Parse(theme.preamble)
	if err != nil {
		return "", fmt.Errorf("typst: parse preamble template: %w", err)
	}
	var preambleBuf bytes.Buffer
	if err := preambleTmpl.Execute(&preambleBuf, typstDocData{Title: opts.Title}); err != nil {
		return "", fmt.Errorf("typst: render preamble: %w", err)
	}

	var titleBlock string
	if opts.Title != "" && !omitTitle {
		titleBlock = "= " + opts.Title + "\n\n"
	}

	return preambleBuf.String() + "\n" + override + titleBlock + body, nil
}

// typstOutput selects the output kind for compileTypst. Format must be
// "pdf" or "png". PPI is honored only for PNG; 0 falls back to 144.
type typstOutput struct {
	Format string
	PPI    int
}

// pandocConvert runs pandoc to convert content from inputFmt to typst markup.
// When toc is true, --toc and --toc-depth=3 are appended so pandoc emits a
// table-of-contents block at the top of the typst output.
func pandocConvert(ctx context.Context, content, inputFmt string, toc bool) (string, error) {
	// Allowlist inputFmt before passing it to exec.Command. The gosec linter
	// correctly flags shell-injection risk on user-controlled strings passed
	// as CLI arguments; restricting to a known-safe set eliminates the risk.
	switch inputFmt {
	case "markdown", "html":
		// accepted
	default:
		return "", fmt.Errorf("pandoc: unsupported input format %q (must be \"markdown\" or \"html\")", inputFmt)
	}

	bin := resolveEnvOrPath(resolveBinaryEnvPandoc, legacyEnvPandoc, "pandoc")
	if bin == "" {
		return "", fmt.Errorf("pandoc binary not found (set %s or ensure pandoc is on PATH)", resolveBinaryEnvPandoc)
	}

	pCtx, cancel := context.WithTimeout(ctx, pandocTimeout)
	defer cancel()

	args := []string{"-f", inputFmt, "-t", "typst", "--wrap=none"}
	if toc {
		args = append(args, "--toc", "--toc-depth=3")
	}
	//nolint:gosec // bin resolved from env/PATH; inputFmt restricted to allowlist above
	cmd := exec.CommandContext(pCtx, bin, args...)
	cmd.Stdin = strings.NewReader(content)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("pandoc exit: %w", err)
	}

	// pandoc emits <label> anchors after headings — drop them so typst
	// doesn't warn about duplicate labels on repeated section names.
	lines := strings.Split(string(out), "\n")
	filtered := lines[:0]
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "<") && strings.HasSuffix(trimmed, ">") {
			continue
		}
		filtered = append(filtered, l)
	}
	return sanitizeTypstFromPandoc(strings.Join(filtered, "\n")), nil
}

// Pandoc/Typst compatibility shims (verified pandoc 3.1.3 / typst 0.14.2 on 2026-04-25):
//
//	pandoc emit                      → rewritten to                   reason
//	---------------------------------+--------------------------------+--------------------------------
//	#horizontalrule (own line)       → #line(length: 100%)            no horizontalrule symbol in stock typst
//	#blockquote[…]                   → #quote(block: true)[…]         no blockquote function in stock typst
//	#cite("name")                    → #cite(<name>)                  typst 0.14+ requires labels, not strings
//	#cite(<name>) (no #bibliography) → [\@name] plaintext             no-bibliography fallback (4th pandoc-ism)
//	#image("http(s)://…")            → [image] plaintext marker       typst has no HTTP loader, can't fetch remote URLs
//
// When pandoc ≥ 3.5 ships in the build environment, retest — its typst writer
// emits compatible markup natively and the first three shims should retire.
// The 4th and 5th shims stay: typst's design (no bibliography injection, no
// network) is independent of pandoc version.
var (
	// (?m) anchors ^/$ to lines, not the whole string, so we only match
	// `#horizontalrule` when it occupies its own line. A mid-line mention
	// (e.g. inside a code block or prose) is left untouched.
	pandocHorizontalRuleRe = regexp.MustCompile(`(?m)^#horizontalrule[ \t]*$`)

	// `#cite("x")` with a typst-label-safe identifier (alnum, _, -, .).
	// Identifiers containing whitespace, parens or brackets are intentionally
	// left alone — typst will error loudly rather than silently mangling.
	pandocCiteStringRe = regexp.MustCompile(`#cite\("([A-Za-z0-9_.\-]+)"\)`)

	// Same identifier shape, but in label form. Used by the no-bibliography
	// fallback below. Only applied when the document does not declare a
	// `#bibliography(...)` block — otherwise the label form is correct.
	pandocCiteLabelRe = regexp.MustCompile(`#cite\(<([A-Za-z0-9_.\-]+)>\)`)

	// Remote `#image(...)` calls. Typst has no HTTP loader; for any
	// http(s):// URL it tries to read a local file and fails ("file not
	// found"). Pandoc emits these for `![alt](URL)` markdown, often wrapped
	// in `#figure([#image(...)], caption: [alt])`. Replacing the inner
	// `#image(...)` with the `[image]` marker keeps the surrounding figure
	// + caption (so the alt text remains visible to the reader).
	pandocRemoteImageRe = regexp.MustCompile(`#image\("https?://[^"]+"\)`)
)

// sanitizeTypstFromPandoc rewrites pandoc-emitted Typst markup that uses
// identifiers from pandoc's typst writer template that aren't present in
// stock Typst 0.14. Each rule is documented with the pandoc → typst delta
// and the reason it's needed; verified empirically with pandoc 3.1.3 +
// typst 0.14.2 on 2026-04-25. See the comment block above the regex
// declarations for the full rule table.
func sanitizeTypstFromPandoc(in string) string {
	// Rule 1: horizontal rule directive → typst line primitive.
	out := pandocHorizontalRuleRe.ReplaceAllString(in, "#line(length: 100%)")

	// Rule 2: blockquote function call → quote function call. The literal
	// `#blockquote[` form is unambiguous (it's a function-call shape) so a
	// plain string replace is safe; mentions of the bare word "#blockquote"
	// in prose or code do not match because the trailing `[` is required.
	out = strings.ReplaceAll(out, "#blockquote[", "#quote(block: true)[")

	// Rule 3: cite("name") string-arg → cite(<name>) label form (per typst 0.14
	// signature). Identifiers with characters typst rejects as labels are left
	// untouched so the failure is loud rather than silent.
	out = pandocCiteStringRe.ReplaceAllString(out, "#cite(<$1>)")

	// Rule 4 (4th pandoc-ism, no-bibliography fallback): typst's `#cite(<x>)`
	// requires a `#bibliography(...)` directive to resolve the label, but the
	// render pipeline never injects one. Without it, the label-form rewrite
	// from rule 3 still errors at compile time ("the document does not contain
	// a bibliography"). When no bibliography is declared, downgrade every
	// surviving label-form cite to a visible plaintext marker `[\@name]` —
	// this matches what pandoc emits for the same source when --citeproc is
	// disabled, and renders cleanly in stock typst. The `\@` escape is
	// required because bare `@name` is parsed as a label reference.
	if !strings.Contains(out, "#bibliography(") {
		out = pandocCiteLabelRe.ReplaceAllString(out, `[\@$1]`)
	}

	// Rule 5: remote images. Typst can only read local paths; an http(s)://
	// URL produces a "file not found" error. Replace the inner #image() call
	// with the literal text marker `[image]` so any surrounding figure block
	// + caption keeps rendering — the caption usually carries the alt text,
	// preserving most of the author's intent.
	out = pandocRemoteImageRe.ReplaceAllString(out, "[image]")

	return out
}

// compileTypst writes the .typ source to a temp file and runs typst compile.
// out.Format selects "pdf" (default) or "png"; PPI applies to PNG only.
func compileTypst(ctx context.Context, source string, out typstOutput) ([]byte, error) {
	bin := resolveEnvOrPath(resolveBinaryEnvTypst, legacyEnvTypst, "typst")
	if bin == "" {
		return nil, fmt.Errorf("typst binary not found (set %s or ensure typst is on PATH)", resolveBinaryEnvTypst)
	}

	format := out.Format
	if format == "" {
		format = typstFormatPDF
	}
	if format != typstFormatPDF && format != typstFormatPNG {
		return nil, fmt.Errorf("typst: unsupported output format %q", format)
	}

	dir, err := os.MkdirTemp("", "gokit-typst-*")
	if err != nil {
		return nil, fmt.Errorf("typst: mkdir temp: %w", err)
	}
	defer os.RemoveAll(dir)

	src := filepath.Join(dir, "doc.typ")
	dst := filepath.Join(dir, "doc."+format)

	if err := os.WriteFile(src, []byte(source), 0600); err != nil {
		return nil, fmt.Errorf("typst: write source: %w", err)
	}

	args := []string{"compile", "-f", format, src, dst}
	if format == typstFormatPNG {
		ppi := out.PPI
		if ppi <= 0 {
			ppi = 144
		}
		args = append(args, "--ppi", strconv.Itoa(ppi))
	}

	tCtx, cancel := context.WithTimeout(ctx, typstTimeout)
	defer cancel()

	start := time.Now()
	//nolint:gosec // bin from env/PATH; src/dst are our own temp paths
	cmd := exec.CommandContext(tCtx, bin, args...)
	stderr, err := cmd.CombinedOutput()
	if err != nil {
		// PNG output cannot represent multi-page documents — surface a
		// caller-actionable hint instead of typst's internal wording.
		s := string(stderr)
		if format == typstFormatPNG && strings.Contains(s, "multiple images without a page number template") {
			return nil, fmt.Errorf("typst rendered multiple pages but image output supports 1 — reduce content or render as PDF (typst stderr: %s)", strings.TrimSpace(s))
		}
		return nil, fmt.Errorf("typst compile: %w\nstderr: %s", err, stderr)
	}
	slog.Info("typst compile ok",
		"elapsed_ms", time.Since(start).Milliseconds(),
		"source_len", len(source),
		"format", format,
	)

	return os.ReadFile(dst)
}

// resolveEnvOrPath checks envKey first, then legacyKey (with a deprecation
// warning), then falls back to exec.LookPath(name).
//
// legacyKey is the pre-v0.92.0 VAELOR_* env var name. Pass "" to skip the
// legacy check.
func resolveEnvOrPath(envKey, legacyKey, name string) string {
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	if legacyKey != "" {
		if v := os.Getenv(legacyKey); v != "" {
			slog.Warn("render/typst: deprecated env var in use; rename to the new key",
				"deprecated", legacyKey, "use_instead", envKey)
			return v
		}
	}
	p, _ := exec.LookPath(name)
	return p
}
