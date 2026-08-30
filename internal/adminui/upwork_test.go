package adminui

import (
	"bytes"
	"fmt"
	"html/template"
	"os"
	"strings"
	"testing"

	"github.com/anatolykoptev/go_job/internal/engine/jobs"
)

func TestBuildUpworkPageData_Mapping(t *testing.T) {
	profile := &jobs.ResumeProfileResult{
		Headline:        "Staff Software Engineer",
		Summary:         "I build distributed systems.",
		HourlyRateCents: 15000,
		Skills: []jobs.SkillSummary{
			{Name: "Go"}, {Name: "Rust"}, {Name: "TypeScript"},
			{Name: "Svelte"}, {Name: "Postgres"}, {Name: "Redis"},
			{Name: "Kafka"}, {Name: "Docker"}, {Name: "K8s"},
			{Name: "gRPC"}, {Name: "WebRTC"}, {Name: "ONNX"},
			{Name: "Prometheus"}, {Name: "Grafana"}, {Name: "Terraform"},
			{Name: "ExtraSkill16"},
		},
		Experiences: []jobs.ExperienceSummary{
			{Title: "Staff SWE", Company: "Acme", StartDate: "2020-01", EndDate: "Present"},
		},
	}

	d := buildUpworkPageData(profile)

	if d.Title != "Staff Software Engineer" {
		t.Errorf("Title: got %q want %q", d.Title, "Staff Software Engineer")
	}
	if d.Rate != "$150.00/hr" {
		t.Errorf("Rate: got %q want $150.00/hr", d.Rate)
	}
	if len(d.Skills) != 15 {
		t.Errorf("Skills len: got %d want 15", len(d.Skills))
	}
	if !d.SkillsOver {
		t.Error("SkillsOver: expected true for 16 skills")
	}
	if d.SkillCount != 16 {
		t.Errorf("SkillCount: got %d want 16", d.SkillCount)
	}
	for _, s := range d.Skills {
		if s == "ExtraSkill16" {
			t.Error("ExtraSkill16 should have been capped out")
		}
	}
	if len(d.Employment) != 1 || d.Employment[0].Title != "Staff SWE" {
		t.Errorf("Employment: %+v", d.Employment)
	}
}

func TestBuildUpworkPageData_EmptyRate(t *testing.T) {
	profile := &jobs.ResumeProfileResult{HourlyRateCents: 0}
	d := buildUpworkPageData(profile)
	if d.Rate != "" {
		t.Errorf("Rate should be empty for 0 cents, got %q", d.Rate)
	}
}

func TestBuildUpworkPageData_SkillsUnderCap(t *testing.T) {
	profile := &jobs.ResumeProfileResult{
		Skills: []jobs.SkillSummary{{Name: "Go"}, {Name: "Rust"}},
	}
	d := buildUpworkPageData(profile)
	if d.SkillsOver {
		t.Error("SkillsOver should be false for 2 skills")
	}
	if len(d.Skills) != 2 {
		t.Errorf("Skills len: got %d want 2", len(d.Skills))
	}
}

func TestUpworkFitnessFunction_NoOsReadFile(t *testing.T) {
	src, err := os.ReadFile("upwork.go")
	if err != nil {
		t.Fatalf("read upwork.go: %v", err)
	}
	if strings.Contains(string(src), "os.ReadFile") {
		t.Error("upwork.go must not use os.ReadFile (DB-only page)")
	}
	if strings.Contains(string(src), "APPLICATIONS_DIR") {
		t.Error("upwork.go must not reference APPLICATIONS_DIR (DB-only page)")
	}
}

func TestUpworkFitnessFunction_NoTemplateHTML(t *testing.T) {
	src, err := os.ReadFile("upwork.go")
	if err != nil {
		t.Fatalf("read upwork.go: %v", err)
	}
	if strings.Contains(string(src), "template.HTML") {
		t.Error("upwork.go must not use template.HTML (DB strings must go through auto-escape)")
	}
}

// TestUpworkTmpl_CharChips asserts that the template renders charClass/charLabel chips
// for both title and overview when those fields are populated.
// Red-on-revert: remove {{charClass .TitleLen 70}} from upworkTmplSrc → "cc-" absent.
func TestUpworkTmpl_CharChips(t *testing.T) {
	// Wire sharedPartialsSrc so {{template "copyBlock" .}} resolves (Phase 3 idiom).
	tmpl := template.Must(template.New("upwork").Funcs(adminuiFuncMap).Parse(sharedPartialsSrc))
	template.Must(tmpl.Parse(upworkTmplSrc))

	profile := &jobs.ResumeProfileResult{
		Headline: "Staff Software Engineer",
		Summary:  "Experienced in distributed systems.",
	}
	d := buildUpworkPageData(profile)

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, d); err != nil {
		t.Fatalf("template execute: %v", err)
	}
	out := buf.String()

	// Both title and overview must emit a cc-* chip span.
	if !strings.Contains(out, "cc-") {
		t.Error("expected cc-* chip span in rendered output for title and overview")
	}
	// charLabel for title: "23 / 70" (len("Staff Software Engineer") == 23)
	if !strings.Contains(out, "23 / 70") {
		t.Errorf("expected title char label %q in output", "23 / 70")
	}
	// charLabel for overview: length / 5000
	overviewLen := len([]rune("Experienced in distributed systems."))
	overviewLabel := fmt.Sprintf("%d / 5000", overviewLen)
	if !strings.Contains(out, overviewLabel) {
		t.Errorf("expected overview char label %q in output", overviewLabel)
	}
}

// TestUpworkTmpl_Portfolio asserts that Projects are rendered in the Portfolio section.
// Red-on-revert: remove Portfolio mapping from buildUpworkPageData → portfolio rows absent.
func TestUpworkTmpl_Portfolio(t *testing.T) {
	// Wire sharedPartialsSrc so {{template "copyBlock" .}} resolves (Phase 3 idiom).
	tmpl := template.Must(template.New("upwork").Funcs(adminuiFuncMap).Parse(sharedPartialsSrc))
	template.Must(tmpl.Parse(upworkTmplSrc))

	profile := &jobs.ResumeProfileResult{
		Projects: []jobs.ProjectSummary{
			{Name: "go-relay", Tech: []string{"Go", "WebRTC"}, URL: "https://github.com/example/go-relay"},
			{Name: "svelte-ui", Tech: []string{"Svelte"}, URL: ""},
		},
	}
	d := buildUpworkPageData(profile)

	if len(d.Portfolio) != 2 {
		t.Fatalf("Portfolio len: got %d want 2", len(d.Portfolio))
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, d); err != nil {
		t.Fatalf("template execute: %v", err)
	}
	out := buf.String()

	for _, want := range []string{"go-relay", "Go, WebRTC", "https://github.com/example/go-relay", "svelte-ui", "Svelte"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in portfolio output", want)
		}
	}
}

// TestCentsToDollars verifies the centsToDollars helper across key boundary values.
// Red-on-revert: remove centsToDollars → compile failure; change formatting → table fails.
func TestCentsToDollars(t *testing.T) {
	cases := []struct {
		cents int64
		want  string
	}{
		{0, ""},
		{15000, "$150.00"},
		{100, "$1.00"},
		{1, "$0.01"},
		{9999, "$99.99"},
	}
	for _, tc := range cases {
		got := centsToDollars(tc.cents)
		if got != tc.want {
			t.Errorf("centsToDollars(%d) = %q, want %q", tc.cents, got, tc.want)
		}
	}
}

// TestUpworkTmpl_TemplateSourceSafety asserts the template source string uses
// plain text rendering (not template.HTML). Paste blocks now render via the
// shared copyBlock partial (Phase 3); the overview edit form still uses <textarea>.
// Red-on-revert: reintroduce template.HTML cast → test fails.
func TestUpworkTmpl_TemplateSourceSafety(t *testing.T) {
	if strings.Contains(upworkTmplSrc, "template.HTML") {
		t.Error("upworkTmplSrc must not use template.HTML (content goes through auto-escape)")
	}
	// The edit form's overview <textarea> must still be present.
	if !strings.Contains(upworkTmplSrc, "<textarea") {
		t.Error("upworkTmplSrc must contain <textarea for the overview edit form")
	}
	// Paste blocks now use copyBlock partial — readonly textarea is gone.
	// The copyBlock template in sharedPartialsSrc renders a <pre> + button instead.
	if strings.Contains(upworkTmplSrc, `<textarea class="uw-textarea" readonly`) {
		t.Error(`paste block textarea should have been replaced with {{template "copyBlock" .}}`)
	}
}

// TestUpworkTmpl_CopyBlocks asserts that paste blocks are rendered via the shared
// copyBlock partial, emitting .gd-copy-btn markup with data-copy-pre / data-copy-field
// attributes and that sharedCSS is included. Content must be HTML-escaped (never raw HTML).
//
// RED before Phase 3: UWCopyBlocks field does not exist on upworkPageData yet.
// GREEN after: sharedPartialsSrc wired in, CopyBlockVMs built, {{template "copyBlock" .}} emitted.
func TestUpworkTmpl_CopyBlocks(t *testing.T) {
	// Synthetic paste blocks — one with angle-bracket content to verify HTML escaping.
	pasteBlocks := []jobs.UpworkPasteBlock{
		{Label: "Test Label", Content: "<script>alert(x)</script>"},
		{Label: "Second Block", Content: "plain text content"},
	}

	// Build CopyBlockVMs the same way the handler will after Phase 3.
	copyVMs := make([]CopyBlockVM, len(pasteBlocks))
	for i, b := range pasteBlocks {
		copyVMs[i] = CopyBlockVM{
			PreID:    fmt.Sprintf("uw-paste-%d", i),
			FieldNum: i,
			Content:  b.Content,
			Label:    b.Label,
		}
	}

	data := upworkPageData{
		NavID:         navIDUpwork,
		UWPasteBlocks: pasteBlocks,
		UWCopyBlocks:  copyVMs, // Phase 3 field — does not exist yet (RED).
	}

	// Build template with shared partials (Phase 3 idiom).
	tmpl := template.Must(
		template.New("upwork").Funcs(adminuiFuncMap).Parse(sharedPartialsSrc),
	)
	if _, err := tmpl.Parse(upworkTmplSrc); err != nil {
		t.Fatalf("parse upworkTmplSrc: %v", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("template execute: %v", err)
	}
	out := buf.String()

	// 1. copyBlock partial must emit .gd-copy-btn
	if !strings.Contains(out, `class="gd-copy-btn"`) {
		t.Error(`expected class="gd-copy-btn" in rendered output (copyBlock partial)`)
	}
	// 2. PreID for index 0
	if !strings.Contains(out, `data-copy-pre="uw-paste-0"`) {
		t.Error(`expected data-copy-pre="uw-paste-0" for first paste block`)
	}
	// 3. data-copy-field for index 0
	if !strings.Contains(out, `data-copy-field="0"`) {
		t.Error(`expected data-copy-field="0" for first paste block`)
	}
	// 4. sharedCSS must be present: .gd-copy-btn{ CSS rule
	if !strings.Contains(out, ".gd-copy-btn{") {
		t.Error("expected .gd-copy-btn{ CSS rule from sharedCSS partial")
	}
	// 5. Content must be HTML-escaped (angle brackets -> &lt; &gt;)
	if strings.Contains(out, "<script>") {
		t.Error("content must be HTML-escaped: raw <script> must not appear in output")
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Error("expected &lt;script&gt; in output (HTML-escaped content)")
	}
}
