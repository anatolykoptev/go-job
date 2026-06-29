package adminui

import (
	"fmt"
	"html"
	"strings"

	"github.com/anatolykoptev/go_job/internal/hunt"
)

// stageOptgroupHTML returns the inner HTML for a stage <select> element,
// structured as two <optgroup> blocks: "Triage" (TriageStages) and
// "Pipeline" (PipelineStages). Option values are html.EscapeString-wrapped
// as a matter of style (stage values are author-constant).
//
// currentStage is used only as an equality key to apply the `selected`
// attribute — it is never interpolated into HTML as text.
//
// Out-of-enum handling:
//   - currentStage == "" → disabled/hidden placeholder "— stage —" is prepended
//     and marked selected so the browser shows a prompt rather than defaulting
//     to the first real option.
//   - currentStage != "" and not in AllStages → disabled/hidden sentinel
//     "current: {escaped}" is prepended so the operator sees the real value
//     without a no-op ✓ save silently overwriting it.
//
// CSS: per-option background/color match the parent select's CSS vars
// (--bg-elevated / --text-primary) for WebKit native-picker compatibility.
func stageOptgroupHTML(currentStage string) string {
	var sb strings.Builder

	// Out-of-enum sentinel / placeholder.
	if !inStageEnum(currentStage) {
		if currentStage == "" {
			sb.WriteString(`<option value="" disabled hidden selected>— stage —</option>`)
		} else {
			fmt.Fprintf(&sb,
				`<option value="" disabled hidden selected style="background:var(--bg-elevated,#1a2540);color:var(--text-primary,#e8edf5)">current: %s</option>`,
				html.EscapeString(currentStage),
			)
		}
	}

	// Triage optgroup.
	sb.WriteString(`<optgroup label="Triage">`)
	for _, s := range hunt.TriageStages {
		selected := ""
		if s == currentStage {
			selected = ` selected`
		}
		fmt.Fprintf(&sb,
			`<option value="%s"%s style="background:var(--bg-elevated,#1a2540);color:var(--text-primary,#e8edf5)">%s</option>`,
			html.EscapeString(s), selected, html.EscapeString(s),
		)
	}
	sb.WriteString(`</optgroup>`)

	// Pipeline optgroup.
	sb.WriteString(`<optgroup label="Pipeline">`)
	for _, s := range hunt.PipelineStages {
		selected := ""
		if s == currentStage {
			selected = ` selected`
		}
		fmt.Fprintf(&sb,
			`<option value="%s"%s style="background:var(--bg-elevated,#1a2540);color:var(--text-primary,#e8edf5)">%s</option>`,
			html.EscapeString(s), selected, html.EscapeString(s),
		)
	}
	sb.WriteString(`</optgroup>`)

	return sb.String()
}

// inStageEnum reports whether s is a member of hunt.AllStages.
func inStageEnum(s string) bool {
	for _, v := range hunt.AllStages {
		if v == s {
			return true
		}
	}
	return false
}
