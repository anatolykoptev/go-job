package components

import (
	"fmt"
	"strings"

	"github.com/a-h/templ"
)

// Pure server-side SVG sparkline — no JS, no chart library. Renders a
// polyline normalised to fit 80x20 px. Zero inputs produce a flat middle
// line so empty rows stay visually calm instead of jumping to top or bottom.
//
// Only numeric format specifiers (%d / %.1f) enter the raw SVG block,
// preventing SVG injection through series values.
const (
	sparkWidth  = 80
	sparkHeight = 20
	sparkPad    = 2

	// sparkGrowBase / sparkGrowPerPoint are hints for strings.Builder.Grow.
	// Each polyline point is at most "XX.X,YY.Y " ≈ 12 bytes; 32 covers the
	// SVG wrapper markup.
	sparkGrowBase     = 32
	sparkGrowPerPoint = 12
)

// Sparkline returns an inline <svg> templ.Component for the given daily
// counts. Callers pass oldest → newest ordering. Length-agnostic; any
// slice of ints works, the curve rescales horizontally.
//
// Ported verbatim from oxpulse-admin/internal/admin/sparkline.go:1-70.
// Changes: package admin→components, exported, returns templ.Component via
// templ.Raw, drops local max() (Go 1.21+ builtin).
func Sparkline(values []int) templ.Component {
	if len(values) == 0 {
		return templ.Raw(fmt.Sprintf(
			`<svg width="%d" height="%d" class="sparkline" aria-hidden="true"></svg>`,
			sparkWidth, sparkHeight,
		))
	}

	var maxV int
	for _, v := range values {
		if v > maxV {
			maxV = v
		}
	}
	// All zeros — render a thin baseline so the row still has visual
	// breadth and the column width is preserved.
	if maxV == 0 {
		midY := sparkHeight / 2
		return templ.Raw(fmt.Sprintf(
			`<svg width="%d" height="%d" class="sparkline" aria-hidden="true">`+
				`<line x1="0" y1="%d" x2="%d" y2="%d" class="sparkline-zero"/></svg>`,
			sparkWidth, sparkHeight, midY, sparkWidth, midY,
		))
	}

	n := len(values)
	step := float64(sparkWidth-2*sparkPad) / float64(max(n-1, 1))
	plotH := float64(sparkHeight - 2*sparkPad)

	var sb strings.Builder
	sb.Grow(sparkGrowBase + n*sparkGrowPerPoint)
	for i, v := range values {
		x := float64(sparkPad) + float64(i)*step
		// Invert Y — SVG origin is top-left; higher count = higher pixel
		// (smaller y).
		y := float64(sparkPad) + plotH*(1-float64(v)/float64(maxV))
		if i > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(&sb, "%.1f,%.1f", x, y)
	}
	points := sb.String()

	return templ.Raw(fmt.Sprintf(
		`<svg width="%d" height="%d" class="sparkline" aria-hidden="true">`+
			`<polyline fill="none" points="%s" class="sparkline-line"/></svg>`,
		sparkWidth, sparkHeight, points,
	))
}
