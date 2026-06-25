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

// TestFindApplicationSlugFromEntries_Match verifies slug prefix matching.
func TestFindApplicationSlugFromEntries_Match(t *testing.T) {
	root := t.TempDir()
	// Create a slug directory.
	slugName := "acme-corp-software-engineer-2026"
	slugDir := filepath.Join(root, slugName)
	if err := os.MkdirAll(slugDir, 0o755); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}

	got, err := findApplicationSlugFromEntries(entries, "Acme Corp", "Software Engineer")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != slugName {
		t.Errorf("got %q, want %q", got, slugName)
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
