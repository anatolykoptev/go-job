package adminui

import (
	"fmt"
	"html"
	"slices"
	"strings"

	"github.com/anatolykoptev/go_job/internal/hunt"
)

// attrSelected is the HTML attribute fragment appended to an <option> when it
// is the currently selected value. Extracted as a const to satisfy goconst (4+ uses).
const attrSelected = ` selected`

// pipelineOptgroupHTML returns inner HTML for a pipeline-only <select> element.
// Used in the jobs-table inline dropdown (migration 012: triage managed separately).
//
// currentStage is the hunt_ratings.stage value (” = not in pipeline).
func pipelineOptgroupHTML(currentStage string) string {
	var sb strings.Builder

	if !slices.Contains(hunt.PipelineStages, currentStage) {
		if currentStage == "" {
			sb.WriteString(`<option value=""` + attrSelected + `>— pipeline —</option>`)
		} else {
			fmt.Fprintf(&sb,
				`<option value="" disabled hidden`+attrSelected+` style="background:var(--bg-elevated,#1a2540);color:var(--text-primary,#e8edf5)">current: %s</option>`,
				html.EscapeString(currentStage),
			)
		}
	} else {
		// Blank "clear" option at top so operator can remove job from pipeline.
		sb.WriteString(`<option value="">— clear —</option>`)
	}

	appendOptgroup(&sb, "Pipeline", hunt.PipelineStages, currentStage)
	return sb.String()
}

// triageSelectOptionsHTML returns inner HTML for a triage-only <select> element.
// Used in the detail-page Triage form (migration 012).
//
// currentTriage is the hunt_ratings.triage value (” = untriaged).
// A blank option labelled "— none —" is always prepended so the operator can
// clear the triage signal.
func triageSelectOptionsHTML(currentTriage string) string {
	var sb strings.Builder

	// Always include a blank "clear" option first.
	selectedBlank := ""
	if !slices.Contains(hunt.TriageStages, currentTriage) {
		selectedBlank = attrSelected
	}
	fmt.Fprintf(&sb, `<option value=""%s>— none —</option>`, selectedBlank)

	for _, s := range hunt.TriageStages {
		sel := ""
		if s == currentTriage {
			sel = attrSelected
		}
		fmt.Fprintf(&sb,
			`<option value="%s"%s style="background:var(--bg-elevated,#1a2540);color:var(--text-primary,#e8edf5)">%s</option>`,
			html.EscapeString(s), sel, html.EscapeString(s),
		)
	}
	return sb.String()
}

// appendOptgroup writes a single <optgroup> block into sb.
// current is used only as an equality key for the `selected` attribute.
func appendOptgroup(sb *strings.Builder, label string, stages []string, current string) {
	fmt.Fprintf(sb, `<optgroup label="%s">`, html.EscapeString(label))
	for _, s := range stages {
		sel := ""
		if s == current {
			sel = attrSelected
		}
		fmt.Fprintf(sb,
			`<option value="%s"%s style="background:var(--bg-elevated,#1a2540);color:var(--text-primary,#e8edf5)">%s</option>`,
			html.EscapeString(s), sel, html.EscapeString(s),
		)
	}
	sb.WriteString(`</optgroup>`)
}
