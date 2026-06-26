package adminui

import (
	"os"
	"path/filepath"
	"testing"
)

// TestValidatePathUnderRoot_Traversal verifies that a symlink-escape path is rejected.
// Red-on-revert: remove ValidatePathUnderRoot and this test will fail to compile;
// change it to always return true and the traversal case will fail.
func TestValidatePathUnderRoot_Traversal(t *testing.T) {
	root := t.TempDir()

	// Attempt to escape root via traversal string.
	dangerous := filepath.Join(root, "..", "..", "etc", "passwd")
	if ValidatePathUnderRoot(root, dangerous) {
		t.Error("expected false for path traversal escape, got true")
	}
}

// TestValidatePathUnderRoot_Valid verifies that a real file inside root is accepted.
func TestValidatePathUnderRoot_Valid(t *testing.T) {
	root := t.TempDir()
	f, err := os.CreateTemp(root, "safe*.txt")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	if !ValidatePathUnderRoot(root, f.Name()) {
		t.Errorf("expected true for %q under %q, got false", f.Name(), root)
	}
}

// TestValidatePathUnderRoot_Missing verifies that a missing file returns false
// (dangling path is not safe to serve).
func TestValidatePathUnderRoot_Missing(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "nonexistent.pdf")
	if ValidatePathUnderRoot(root, missing) {
		t.Error("expected false for missing file, got true")
	}
}

// TestFindApplicationPDF_SubmitSubdir verifies Stage 1: canonical submit/ path found.
// Red-on-revert: delete the submit/ branch in findApplicationPDF → this returns "".
func TestFindApplicationPDF_SubmitSubdir(t *testing.T) {
	root := t.TempDir()
	submitDir := filepath.Join(root, "submit")
	if err := os.MkdirAll(submitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(submitDir, "resume-v1.pdf")
	if err := os.WriteFile(want, []byte("%PDF"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := findApplicationPDF(root, "resume")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestFindApplicationPDF_LegacyRoot verifies Stage 2: root fallback used when submit/ empty.
func TestFindApplicationPDF_LegacyRoot(t *testing.T) {
	root := t.TempDir()
	want := filepath.Join(root, "cover-v2.pdf")
	if err := os.WriteFile(want, []byte("%PDF"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := findApplicationPDF(root, "cover")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestFindApplicationPDF_Missing verifies "" when no PDF exists in either location.
func TestFindApplicationPDF_Missing(t *testing.T) {
	root := t.TempDir()
	got := findApplicationPDF(root, "resume")
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

// dirEntries creates a temp dir with the named subdirectories and returns its entries.
func dirEntries(t *testing.T, names ...string) []os.DirEntry {
	t.Helper()
	root := t.TempDir()
	for _, n := range names {
		if err := os.MkdirAll(filepath.Join(root, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

// TestFindApplicationSlugFromEntries_TokenOmission: a curated dir omits the
// less-distinctive title tokens; the token-subset matcher must still resolve it
// (a naive full-title HasPrefix would 404).
func TestFindApplicationSlugFromEntries_TokenOmission(t *testing.T) {
	entries := dirEntries(t, "anthropic-databases", "openai-gpu-infra")
	got, err := findApplicationSlugFromEntries(entries, "Anthropic", "Staff Software Engineer, Databases")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "anthropic-databases" {
		t.Errorf("got %q, want %q", got, "anthropic-databases")
	}
}

// TestFindApplicationSlugFromEntries_BestMatch: the most-specific dir wins.
func TestFindApplicationSlugFromEntries_BestMatch(t *testing.T) {
	entries := dirEntries(t, "replit-agent", "replit-staff-agent-platform")
	got, err := findApplicationSlugFromEntries(entries, "Replit", "Staff Agent Platform Engineer")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "replit-staff-agent-platform" {
		t.Errorf("got %q, want %q (most specific)", got, "replit-staff-agent-platform")
	}
}

// TestFindApplicationSlugFromEntries_RejectsExtraToken: a dir token not in the
// job's company+title (e.g. a stray year) means it is a different job.
func TestFindApplicationSlugFromEntries_RejectsExtraToken(t *testing.T) {
	entries := dirEntries(t, "acme-billing-2026")
	if _, err := findApplicationSlugFromEntries(entries, "Acme", "Engineer"); err == nil {
		t.Error("expected no match for a dir with an unrelated token")
	}
}

// TestFindApplicationSlugFromEntries_WrongCompany: the company-slug prefix anchors the match.
func TestFindApplicationSlugFromEntries_WrongCompany(t *testing.T) {
	entries := dirEntries(t, "google-engineer")
	if _, err := findApplicationSlugFromEntries(entries, "Acme", "Engineer"); err == nil {
		t.Error("expected no match when dir does not start with the company slug")
	}
}

// TestFindApplicationSlugFromEntries_AmbiguousRejected: two distinct dirs that
// match a job equally well must NOT silently resolve to one (wrong-PDF guard).
func TestFindApplicationSlugFromEntries_AmbiguousRejected(t *testing.T) {
	entries := dirEntries(t, "anthropic-aa", "anthropic-bb")
	if _, err := findApplicationSlugFromEntries(entries, "Anthropic", "AA BB"); err == nil {
		t.Error("expected ambiguous match to be rejected")
	}
}

// TestScanJobPDFs gates the detail-page links: true only when the PDF exists.
func TestScanJobPDFs(t *testing.T) {
	root := t.TempDir()
	submit := filepath.Join(root, "acme-platform", "submit")
	if err := os.MkdirAll(submit, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(submit, "resume-acme.pdf"), []byte("%PDF"), 0o644); err != nil {
		t.Fatal(err)
	}
	hasResume, hasCover := scanJobPDFs(root, "Acme", "Platform Engineer")
	if !hasResume {
		t.Error("expected hasResume=true")
	}
	if hasCover {
		t.Error("expected hasCover=false (no cover pdf)")
	}
	if hr, hc := scanJobPDFs(root, "Nobody", "Engineer"); hr || hc {
		t.Errorf("expected false,false for unknown company; got %v,%v", hr, hc)
	}
}

// TestFindApplicationSlugFromEntries_NoMatch verifies error when no slug matches.
func TestFindApplicationSlugFromEntries_NoMatch(t *testing.T) {
	root := t.TempDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = findApplicationSlugFromEntries(entries, "Acme", "Engineer")
	if err == nil {
		t.Error("expected error for no match, got nil")
	}
}
