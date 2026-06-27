package applications

// legacy_test.go — unit tests for the legacy fuzzy slug matcher.
//
// Previously these tests lived in adminui/download_test.go when the functions
// were defined there. Moved here as part of the authority consolidation:
// findApplicationSlugFromEntries and findApplicationPDF now live in
// applications/legacy.go and are tested in their home package.
//
// RED-on-revert:
//   - Remove the token-subset matcher → TestFindApplicationSlugFromEntries_TokenOmission fails.
//   - Remove the best-match scorer → TestFindApplicationSlugFromEntries_BestMatch fails.
//   - Remove the company-prefix anchor → TestFindApplicationSlugFromEntries_WrongCompany fails.
//   - Remove the ambiguity guard → TestFindApplicationSlugFromEntries_AmbiguousRejected fails.
//   - Remove the submit/ branch in findApplicationPDF → TestFindApplicationPDF_SubmitSubdir fails.

import (
	"os"
	"path/filepath"
	"testing"
)

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

// TestFindApplicationPDF_SubmitSubdir verifies Stage 1: canonical submit/ path found.
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

// TestFindApplicationSlugFromEntries_TokenOmission: a curated dir omits the
// less-distinctive title tokens; the token-subset matcher must still resolve it.
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

// TestAuthorityLegacyResolve_Found gates the detail-page links: true only when the PDF exists.
// Replaces the old TestScanJobPDFs which tested the now-removed adminui.scanJobPDFs helper;
// this test drives the same semantics through the Authority interface.
// RED-on-revert: remove Authority.LegacyResolve and this test fails to compile.
func TestAuthorityLegacyResolve_Found(t *testing.T) {
	root := t.TempDir()
	submit := filepath.Join(root, "acme-platform", "submit")
	if err := os.MkdirAll(submit, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(submit, "resume-acme.pdf"), []byte("%PDF"), 0o644); err != nil {
		t.Fatal(err)
	}

	auth := New(nil, root)

	if got := auth.LegacyResolve("Acme", "Platform Engineer", KindResume); got == "" {
		t.Error("expected LegacyResolve to find resume, got empty")
	}
	if got := auth.LegacyResolve("Acme", "Platform Engineer", KindCover); got != "" {
		t.Errorf("expected LegacyResolve to return empty for missing cover, got %q", got)
	}
	if got := auth.LegacyResolve("Nobody", "Engineer", KindResume); got != "" {
		t.Errorf("expected empty for unknown company, got %q", got)
	}
}
