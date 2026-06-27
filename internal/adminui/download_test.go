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
