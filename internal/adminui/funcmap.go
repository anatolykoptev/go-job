package adminui

import (
	"fmt"
	"html/template"
)

// tmplFuncCharClass and tmplFuncCharLabel are template.FuncMap keys shared by
// linkedin.go and upwork.go.
const (
	ccAmber           = "cc-amber"
	tmplFuncCharClass = "charClass"
	tmplFuncCharLabel = "charLabel"
)

// charCounterClass returns the CSS class for a char counter chip.
func charCounterClass(count, limit int) string {
	if limit == 0 || count == 0 {
		return "cc-muted"
	}
	pct := float64(count) / float64(limit)
	switch {
	case pct > 1.0:
		return "cc-red"
	case pct >= 0.8:
		return ccAmber
	default:
		return "cc-green"
	}
}

// charCounterLabel returns "134 / 220" or "134 chars" when no limit.
func charCounterLabel(count, limit int) string {
	if limit == 0 {
		return fmt.Sprintf("%d chars", count)
	}
	return fmt.Sprintf("%d / %d", count, limit)
}

// adminuiFuncMap is the shared template.FuncMap for all adminui templates.
var adminuiFuncMap = template.FuncMap{
	tmplFuncCharClass: charCounterClass,
	tmplFuncCharLabel: charCounterLabel,
}
