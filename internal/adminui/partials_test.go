package adminui

import (
	"html/template"
	"strings"
	"testing"
)

// TestSharedPartialsSrc_Parse verifies the shared partials const is valid Go template syntax.
func TestSharedPartialsSrc_Parse(t *testing.T) {
	_, err := template.New("t").Funcs(adminuiFuncMap).Parse(sharedPartialsSrc)
	if err != nil {
		t.Fatalf("sharedPartialsSrc failed to parse: %v", err)
	}
}

// TestCopyBlockPartial_ZeroVM executes copyBlock with a zero-value CopyBlockVM.
func TestCopyBlockPartial_ZeroVM(t *testing.T) {
	tmpl := template.Must(template.New("t").Funcs(adminuiFuncMap).Parse(sharedPartialsSrc))
	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, "copyBlock", CopyBlockVM{}); err != nil {
		t.Fatalf("copyBlock zero-VM execute failed: %v", err)
	}
}

// TestCopyBlockPartial_FullVM executes copyBlock with a fully populated CopyBlockVM
// and asserts required attributes + HTML-escaping of Content.
func TestCopyBlockPartial_FullVM(t *testing.T) {
	tmpl := template.Must(template.New("t").Funcs(adminuiFuncMap).Parse(sharedPartialsSrc))
	vm := CopyBlockVM{
		PreID:     "uw-paste-0",
		Content:   "<b>x</b>",
		FieldNum:  1,
		Label:     "Title",
		CharCount: 5,
		CharLimit: 70,
	}
	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, "copyBlock", vm); err != nil {
		t.Fatalf("copyBlock full-VM execute failed: %v", err)
	}
	out := buf.String()

	checks := []struct {
		name    string
		want    string
		present bool
	}{
		{"has gd-copy-btn class", `class="gd-copy-btn"`, true},
		{"has data-copy-pre", `data-copy-pre="uw-paste-0"`, true},
		{"has data-copy-field", `data-copy-field="1"`, true},
		{"content HTML-escaped (no raw <b>)", "<b>", false},
		{"content HTML-escaped (has &lt;b&gt;)", "&lt;b&gt;", true},
	}
	for _, c := range checks {
		got := strings.Contains(out, c.want)
		if got != c.present {
			if c.present {
				t.Errorf("copyBlock full-VM: expected to find %q in output\noutput:\n%s", c.want, out)
			} else {
				t.Errorf("copyBlock full-VM: expected NOT to find %q in output\noutput:\n%s", c.want, out)
			}
		}
	}
}

// TestCharChipPartial_ZeroVM executes charChip with a zero-value CharChipVM.
func TestCharChipPartial_ZeroVM(t *testing.T) {
	tmpl := template.Must(template.New("t").Funcs(adminuiFuncMap).Parse(sharedPartialsSrc))
	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, "charChip", CharChipVM{}); err != nil {
		t.Fatalf("charChip zero-VM execute failed: %v", err)
	}
}

// TestCharChipPartial_FullVM executes charChip with CharCount > 0 and asserts a cc-* class is present.
func TestCharChipPartial_FullVM(t *testing.T) {
	tmpl := template.Must(template.New("t").Funcs(adminuiFuncMap).Parse(sharedPartialsSrc))
	vm := CharChipVM{CharCount: 50, CharLimit: 70}
	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, "charChip", vm); err != nil {
		t.Fatalf("charChip full-VM execute failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "cc-") {
		t.Errorf("charChip full-VM: expected a cc-* class in output\noutput:\n%s", out)
	}
}
