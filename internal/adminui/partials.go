package adminui

// CopyBlockVM is the view-model for the {{define "copyBlock"}} partial.
//
// PreID/Content/FieldNum/Label/CharCount/CharLimit are the original Upwork
// (#127) contract. CopyField/AriaNoun/TitleHint were added by the LinkedIn
// strangler cutover (Phase 5) so the ONE shared partial reproduces LinkedIn's
// pre-existing byte-for-byte attributes; all three default to the Upwork
// values when empty, so Upwork output is unchanged.
type CopyBlockVM struct {
	PreID     string
	Content   string
	FieldNum  int
	Label     string
	CharCount int
	CharLimit int

	// CopyField overrides the literal data-copy-field attribute value.
	// Empty -> FieldNum (Upwork). LinkedIn passes its section number string
	// (e.g. "3"), where multiple blocks in one section share the section id.
	CopyField string
	// AriaNoun is the no-label aria-label noun ("section" for LinkedIn).
	// Empty -> "field" (Upwork). Only used when Label is empty.
	AriaNoun string
	// TitleHint is inserted before "paste" in the button title
	// ("LinkedIn " for LinkedIn). Empty -> "" (Upwork: "...for paste").
	TitleHint string
}

// CharChipVM is the view-model for the {{define "charChip"}} partial.
type CharChipVM struct {
	CharCount int
	CharLimit int
}

// sharedPartialsSrc contains reusable Go template defines shared across adminui pages.
// Consumed by upwork.go (#127) and linkedin.go (Phase 5 strangler cutover).
const sharedPartialsSrc = `
{{define "sharedCSS"}}
  .li-pre{background:var(--bg-deep,#0f172a);border:1px solid var(--border-subtle,#1e293b);border-radius:var(--radius,.375rem);padding:.875rem 1rem;font-family:var(--font-mono,monospace);font-size:.8125rem;color:var(--text-primary,#f1f5f9);white-space:pre-wrap;word-break:break-word;max-height:18rem;overflow-y:auto;line-height:1.6;margin:0}
  .li-code-wrap{position:relative;margin-bottom:.75rem}
  .gd-copy-btn{position:absolute;top:.5rem;right:.5rem;padding:.25rem .625rem;background:var(--bg-elevated,#1e293b);color:var(--text-secondary,#94a3b8);border:1px solid var(--border,#334155);border-radius:var(--radius,.375rem);font-family:var(--font-mono,monospace);font-size:.6875rem;cursor:pointer;transition:color .15s,background-color .15s,border-color .15s;white-space:nowrap}
  .gd-copy-btn:hover{color:var(--text-primary,#f1f5f9);border-color:var(--text-muted,#64748b)}
  .gd-copy-btn.copied{color:var(--green,#34d399);border-color:var(--green-dim,#052e16);background:var(--green-dim,#052e16)}
  .cc-muted{font-size:.6875rem;color:var(--text-muted,#64748b);margin-left:.375rem}
  .cc-green{font-size:.6875rem;color:var(--green,#34d399);margin-left:.375rem}
  .cc-amber{font-size:.6875rem;color:#f59e0b;margin-left:.375rem}
  .cc-red{font-size:.6875rem;color:#ef4444;margin-left:.375rem}
{{end}}

{{define "charChip"}}{{if gt .CharCount 0}}<span class="{{charClass .CharCount .CharLimit}}">{{charLabel .CharCount .CharLimit}}</span>{{end}}{{end}}

{{define "copyBlock"}}
<div class="li-code-wrap">
  <pre class="li-pre" id="{{.PreID}}">{{.Content}}</pre>
  <button class="gd-copy-btn" type="button"
          data-copy-pre="{{.PreID}}"
          data-copy-field="{{if .CopyField}}{{.CopyField}}{{else}}{{.FieldNum}}{{end}}"
          aria-label="Copy {{if .Label}}{{.Label}}{{else}}{{if .AriaNoun}}{{.AriaNoun}}{{else}}field{{end}} {{if .CopyField}}{{.CopyField}}{{else}}{{.FieldNum}}{{end}}{{end}}"
          title="Paragraph spacing normalized for {{.TitleHint}}paste">Copy</button>
  {{if gt .CharCount 0}}<span class="{{charClass .CharCount .CharLimit}}">{{charLabel .CharCount .CharLimit}}</span>{{end}}
</div>
{{end}}
`
