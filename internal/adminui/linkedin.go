package adminui

import (
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/anatolykoptev/go-panel/render"
	"github.com/anatolykoptev/go-panel/resource"
)

// linkedInCharLimits maps section number (H2 leading digit) to LinkedIn char limits.
// Sections without a known limit show 0 (no counter cap displayed).
var linkedInCharLimits = map[string]int{
	"1": 220,
	"2": 2600,
	"3": 2000, // per entry (A/B/C) — each block independently shows "N / 2000"
	"4": 0,
	"5": 0,
	"6": 0,
	"7": 0,
	"8": 0,
}

// LinkedIn item kinds.
const (
	liItemProse = "prose"
	liItemCode  = "code"
)

// linkedInItem is one ordered element of a section's content.
//
// Kind == liItemProse: only HTML is populated.
// Kind == liItemCode:  Content / CharCount / CharLimit / PreID / Label populated,
//
//	and CopyVM holds the shared-partial view-model for the copy block.
type linkedInItem struct {
	Kind string

	// Prose fields.
	HTML template.HTML

	// Code fields.
	Label     string
	Content   string
	CharCount int
	CharLimit int
	PreID     string

	// CopyVM is the shared copyBlock partial view-model for a code item.
	// Built once in flushFence so the template just calls {{template "copyBlock" .CopyVM}}.
	CopyVM CopyBlockVM
}

// linkedInSection is one parsed H2 section from LINKEDIN-UPDATE.md.
type linkedInSection struct {
	Number    string
	Title     string
	Items     []linkedInItem
	CharLimit int
}

// linkedInPageData is the template data for /admin/linkedin.
type linkedInPageData struct {
	Sections []linkedInSection
	Missing  bool
}

// linkedinHandler serves GET /admin/linkedin.
func linkedinHandler(p *resource.Panel, applicationsDir string) http.HandlerFunc {
	tmpl := template.Must(template.New("linkedin").Funcs(adminuiFuncMap).Parse(sharedPartialsSrc))
	template.Must(tmpl.Parse(linkedinTmplSrc))

	return func(w http.ResponseWriter, r *http.Request) {
		data := buildLinkedInPageData(applicationsDir)
		var buf strings.Builder
		if err := tmpl.Execute(&buf, data); err != nil {
			http.Error(w, "render error", http.StatusInternalServerError)
			return
		}
		if err := p.RenderPageHTML(w, r, "LinkedIn", "linkedin", buf.String()); err != nil {
			slog.Error("adminui: render linkedin", "err", err)
		}
	}
}

// buildLinkedInPageData reads and parses LINKEDIN-UPDATE.md from dir.
// Returns Missing=true when the file is absent — never returns an error.
func buildLinkedInPageData(dir string) linkedInPageData {
	path := filepath.Join(dir, "LINKEDIN-UPDATE.md") //nolint:gosec // fixed filename, no user input
	raw, err := os.ReadFile(path)                    //nolint:gosec // path is server-controlled
	if err != nil {
		return linkedInPageData{Missing: true}
	}
	return linkedInPageData{Sections: parseLinkedInSections(string(raw))}
}

// parseLinkedInSections splits the markdown into H2 sections and emits each
// section's content as an ORDERED sequence of items (prose runs + code blocks)
// in true document order.
//
// Parsing strategy (scanner-based, no regex):
//  1. Scan line by line.
//  2. "## " → start a new section.
//  3. "### " inside a section → flush preceding prose run, record as pending label.
//     If section ends with no fence consuming it, label is folded into prose.
//  4. Toggle fence on/off for lines starting with "```".
//  5. Fence open → flush accumulated prose run first.
//  6. Fence close → emit code item with pending label (cleared after).
//  7. Other lines → accumulate into current prose run.
func parseLinkedInSections(md string) []linkedInSection {
	lines := strings.Split(md, "\n")
	var sections []linkedInSection
	var current *linkedInSection

	var (
		inFence      bool
		blockIdx     int
		pendingLabel string
		codeLines    []string
		proseLines   []string
	)

	// flushProse emits the accumulated prose run as a prose item.
	// foldLabel=true folds any unused pendingLabel into prose so it is not lost.
	// foldLabel=false keeps pendingLabel for the upcoming fence.
	flushProse := func(foldLabel bool) {
		if current == nil {
			proseLines = nil
			return
		}
		if foldLabel && pendingLabel != "" {
			proseLines = append([]string{"### " + pendingLabel}, proseLines...)
			pendingLabel = ""
		}
		src := strings.TrimSpace(strings.Join(proseLines, "\n"))
		proseLines = nil
		if src == "" {
			return
		}
		current.Items = append(current.Items, linkedInItem{
			Kind: liItemProse,
			HTML: render.Markdown(src),
		})
	}

	flushFence := func() {
		if current == nil {
			codeLines = nil
			return
		}
		content := strings.TrimSpace(strings.Join(codeLines, "\n"))
		blockIdx++
		preID := fmt.Sprintf("li-code-%s-%d", current.Number, blockIdx)
		item := linkedInItem{
			Kind:      liItemCode,
			Label:     pendingLabel,
			Content:   content,
			CharCount: len(content),
			CharLimit: current.CharLimit,
			PreID:     preID,
		}
		// Build the shared copyBlock view-model. CopyField/AriaNoun/TitleHint
		// reproduce LinkedIn's pre-strangler byte-for-byte attributes:
		// data-copy-field carries the SECTION number (shared by all blocks in a
		// section), the no-label aria noun is "section", and the title says
		// "LinkedIn paste".
		item.CopyVM = CopyBlockVM{
			PreID:     preID,
			Content:   content,
			Label:     pendingLabel,
			CharCount: item.CharCount,
			CharLimit: item.CharLimit,
			CopyField: current.Number,
			AriaNoun:  "section",
			TitleHint: "LinkedIn ",
		}
		current.Items = append(current.Items, item)
		pendingLabel = ""
		codeLines = nil
	}

	finishSection := func() {
		if current == nil {
			return
		}
		flushProse(true)
		sections = append(sections, *current)
		current = nil
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			if inFence {
				flushFence()
				inFence = false
			}
			finishSection()

			heading := strings.TrimPrefix(line, "## ")
			num, title := parseH2Number(heading)
			limit := linkedInCharLimits[num]
			current = &linkedInSection{
				Number:    num,
				Title:     title,
				CharLimit: limit,
			}
			blockIdx = 0
			pendingLabel = ""
			proseLines = nil
			codeLines = nil
			continue
		}

		if current == nil {
			continue
		}

		if strings.HasPrefix(line, "### ") {
			if inFence {
				codeLines = append(codeLines, line)
			} else {
				flushProse(true)
				pendingLabel = strings.TrimSpace(strings.TrimPrefix(line, "### "))
			}
			continue
		}

		if strings.HasPrefix(line, "```") {
			if inFence {
				flushFence()
				inFence = false
			} else {
				flushProse(false)
				inFence = true
				codeLines = nil
			}
			continue
		}

		if inFence {
			codeLines = append(codeLines, line)
		} else {
			proseLines = append(proseLines, line)
		}
	}

	if inFence {
		flushFence()
	}
	finishSection()

	return sections
}

// parseH2Number splits "1. Headline (max 220 chars)" into ("1", "Headline (max 220 chars)").
// Falls back to ("", heading) when no leading digit.
func parseH2Number(heading string) (num, title string) {
	dot := strings.Index(heading, ".")
	if dot < 1 {
		return "", heading
	}
	candidate := strings.TrimSpace(heading[:dot])
	for _, c := range candidate {
		if c < '0' || c > '9' {
			return "", heading
		}
	}
	return candidate, strings.TrimSpace(heading[dot+1:])
}

// linkedinTmplSrc is the HTML content fragment for the LinkedIn update page.
// The copy-block + char-chip markup and their CSS (.li-pre/.li-code-wrap/
// .gd-copy-btn/.cc-*) live in sharedPartialsSrc (partials.go); this template
// pulls them in via {{template "sharedCSS" .}} + {{template "copyBlock" .CopyVM}}.
const linkedinTmplSrc = `<style>
  .li-section{background:var(--bg-surface,#1e293b);border:1px solid var(--border,#334155);border-radius:var(--radius-lg,.75rem);padding:1.25rem 1.5rem;margin-bottom:1.25rem}
  .li-section h3{font-size:.9375rem;font-weight:700;color:var(--text-primary,#f1f5f9);margin-bottom:.5rem;display:flex;align-items:center;gap:.625rem;flex-wrap:wrap}
  .li-section h3 a{color:inherit;text-decoration:none}
  .li-section h3 a:hover{color:var(--accent,#60a5fa)}
  .li-block-label{font-size:.8125rem;font-weight:600;color:var(--text-primary,#f1f5f9);margin:.5rem 0 .25rem}
  .li-instructions{font-size:.8125rem;color:var(--text-secondary,#94a3b8);line-height:1.65}
  .li-instructions p{margin-bottom:.5rem}
  .li-instructions p:last-child{margin-bottom:0}
  .li-instructions strong{color:var(--text-primary,#f1f5f9);font-weight:600}
  .li-instructions code{font-family:var(--font-mono,monospace);font-size:.75rem;background:var(--bg-elevated,#1e293b);padding:.1rem .35rem;border-radius:.25rem;color:var(--accent,#60a5fa)}
  .li-instructions ul,.li-instructions ol{margin:.25rem 0 .5rem 1.25rem}
  .li-instructions li{margin-bottom:.2rem}
  .li-instructions table{border-collapse:collapse;margin:.5rem 0;font-size:.75rem}
  .li-instructions th,.li-instructions td{border:1px solid var(--border-subtle,#1e293b);padding:.25rem .5rem;text-align:left}
  .li-instructions th{background:var(--bg-elevated,#1e293b);color:var(--text-primary,#f1f5f9);font-weight:600}
  .li-toc{display:flex;gap:.375rem;flex-wrap:wrap;margin-bottom:1.5rem}
  .li-toc a{padding:.25rem .625rem;border:1px solid var(--border,#334155);border-radius:9999px;color:var(--text-secondary,#94a3b8);font-size:.75rem;font-weight:500;text-decoration:none;transition:all .15s}
  .li-toc a:hover{color:var(--text-primary,#f1f5f9);background:var(--bg-elevated,#1e293b);border-color:var(--text-muted,#64748b)}
{{template "sharedCSS" .}}
</style>

<div class="page-header">
  <h2>&#128279; LinkedIn Update</h2>
  <p>Paste-ready content for profile sections &#x2014; open LinkedIn in another tab and update field-by-field.</p>
</div>

{{if .Missing}}
<div style="padding:2rem;text-align:center;color:var(--text-muted,#64748b);font-family:var(--font-mono,monospace);font-size:.875rem">
  <p>No content file found. Expected <code>LINKEDIN-UPDATE.md</code> in the applications directory.</p>
</div>
{{else}}

<div id="copy-feedback" role="status" aria-live="polite" aria-atomic="true" class="sr-only"></div>

<nav class="li-toc" aria-label="Jump to section">
  {{range .Sections}}
  <a href="#li-section-{{.Number}}">{{.Number}}. {{.Title}}</a>
  {{end}}
</nav>

{{range .Sections}}
<div class="li-section" id="li-section-{{.Number}}">
  <h3>
    <a href="#li-section-{{.Number}}">#</a>
    {{.Number}}. {{.Title}}
  </h3>

  {{range .Items}}
  {{if eq .Kind "code"}}
  {{if .Content}}
  {{if .Label}}<div class="li-block-label">{{.Label}}</div>{{end}}
  {{template "copyBlock" .CopyVM}}
  {{end}}
  {{else}}
  <div class="li-instructions">{{.HTML}}</div>
  {{end}}
  {{end}}
</div>
{{end}}

{{end}}`
