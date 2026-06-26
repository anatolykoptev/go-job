package adminui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShortlist_EnrichAndSort(t *testing.T) {
	root := t.TempDir()
	tracker := `{"version":1,"updated":"2026-06-11","jobs":[
		{"score":10,"company":"Acme","title":"Platform Engineer","status":"saved","url":"https://x"},
		{"score":16,"company":"Beta","title":"Staff Engineer","status":"pack-ready","url":"https://y"}
	]}`
	if err := os.WriteFile(filepath.Join(root, "_tracker.json"), []byte(tracker), 0o644); err != nil {
		t.Fatal(err)
	}
	// Beta (pack-ready) has a prepared resume PDF; Acme has none.
	submit := filepath.Join(root, "beta-staff", "submit")
	if err := os.MkdirAll(submit, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(submit, "resume.pdf"), []byte("%PDF"), 0o644); err != nil {
		t.Fatal(err)
	}

	tf, err := loadTracker(root)
	if err != nil {
		t.Fatalf("loadTracker: %v", err)
	}
	entries := enrichShortlist(tf.Jobs, root)
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}
	// pack-ready sorts before saved
	if entries[0].Company != "Beta" {
		t.Errorf("pack-ready should sort first, got %q", entries[0].Company)
	}
	if entries[0].Slug != "beta-staff" || !entries[0].HasResume {
		t.Errorf("Beta should resolve beta-staff with a resume PDF; got slug=%q hasResume=%v", entries[0].Slug, entries[0].HasResume)
	}
	if entries[0].HasCover {
		t.Error("Beta has no cover PDF")
	}
	if entries[1].HasResume || entries[1].HasCover {
		t.Error("Acme has no prepared pack → no PDFs")
	}

	// rendering does not panic and includes the curated company.
	html := renderShortlistHTML(tf, entries, "")
	if !strings.Contains(html, "Beta") || !strings.Contains(html, "/admin/shortlist/beta-staff/download/resume") {
		t.Errorf("rendered shortlist missing expected content")
	}
}

func TestLoadTracker_Missing(t *testing.T) {
	if _, err := loadTracker(t.TempDir()); err == nil {
		t.Error("expected error when _tracker.json absent")
	}
}

func TestShortlist_FilterCounts(t *testing.T) {
	entries := []shortlistEntry{
		{trackerJob: trackerJob{Company: "Acme", Status: "pack-ready"}, HasResume: true},
		{trackerJob: trackerJob{Company: "Beta", Status: "saved"}},
		{trackerJob: trackerJob{Company: "Gamma", Status: "saved"}},
	}
	tf := &trackerFile{Updated: "2026-06-11"}

	all := renderShortlistHTML(tf, entries, "")
	for _, want := range []string{"All 3", "With docs 1", "Pack-ready 1", "Saved 2"} {
		if !strings.Contains(all, want) {
			t.Errorf("chip counts missing %q", want)
		}
	}
	saved := renderShortlistHTML(tf, entries, "saved")
	if strings.Contains(saved, ">Acme<") {
		t.Error("saved filter must exclude pack-ready Acme")
	}
	if !strings.Contains(saved, ">Beta<") || !strings.Contains(saved, ">Gamma<") {
		t.Error("saved filter must include Beta and Gamma")
	}
	docs := renderShortlistHTML(tf, entries, "docs")
	if !strings.Contains(docs, ">Acme<") || strings.Contains(docs, ">Beta<") {
		t.Error("docs filter must include only Acme")
	}
}
