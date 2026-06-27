package applications_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/anatolykoptev/go_job/internal/engine/jobs/applications"
	"github.com/prometheus/client_golang/prometheus"
)

// stubRenderer is a test double for applications.Renderer.
type stubRenderer struct {
	pdf []byte
	err error
}

func (s *stubRenderer) PDF(_ context.Context, _ string) ([]byte, error) {
	return s.pdf, s.err
}

// persistCounter reads the current value of gojob_application_persist_total
// for the given outcome label from the default prometheus registry.
func persistCounter(t *testing.T, outcome string) float64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "gojob_application_persist_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "outcome" && lp.GetValue() == outcome {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// TestExists_NoMkdir verifies that Exists on a fresh (never-persisted) job ID
// returns false WITHOUT creating any directory under UPLOADS_ROOT.
// This guards FIX 3: Resolve/Exists previously called uploads.Path which does
// MkdirAll, littering the uploads volume with empty dirs for every shortlist row.
func TestExists_NoMkdir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("UPLOADS_ROOT", root)

	auth := applications.New(nil, "")
	id := int64(9001) // never persisted

	exists := auth.Exists(id, applications.KindResume)

	if exists {
		t.Fatal("Exists should return false for a never-persisted id")
	}

	// The directory MUST NOT have been created by the read-path.
	appDir := filepath.Join(root, "go-job", "applications", strconv.FormatInt(id, 10))
	if _, statErr := os.Stat(appDir); !os.IsNotExist(statErr) {
		t.Errorf("Exists must not create %q — got err=%v", appDir, statErr)
	}
}

// TestPersist_NilRenderer_OkMdOnly verifies that Persist with renderer=nil
// writes the markdown files and increments the ok_md_only counter.
// Guards the base-case: no PDF step, no crash.
func TestPersist_NilRenderer_OkMdOnly(t *testing.T) {
	root := t.TempDir()
	t.Setenv("UPLOADS_ROOT", root)

	auth := applications.New(nil, "")
	before := persistCounter(t, "ok_md_only")

	res, err := auth.Persist(context.Background(), 1, "# Resume", "# Cover")
	if err != nil {
		t.Fatalf("Persist returned unexpected error: %v", err)
	}
	if res.PDFRendered {
		t.Error("PDFRendered should be false with nil renderer")
	}

	after := persistCounter(t, "ok_md_only")
	if after-before != 1 {
		t.Errorf("ok_md_only counter: want +1, got delta=%.0f", after-before)
	}
}

// TestPersist_WritePDFError verifies that when a renderer succeeds (returns PDF
// bytes) but the write to disk fails (dir is read-only after MD files are
// pre-created), Persist:
//   - does NOT return an error (PDF write failure is non-fatal)
//   - sets result.PDFRendered = false
//   - increments gojob_application_persist_total{outcome="error_pdf_write"}
//     NOT "ok_md_only" — the two cases must be distinguishable.
//
// This guards FIX 2: the original code had an empty err!=nil branch, silently
// masking disk/permission failures as ok_md_only.
//
// Permission setup: pre-create the app dir + MD files while writable, then
// chmod dir to 0o555. writeMD succeeds (overwrites existing 0o644 files; needs
// file write perm, not dir write perm). writePDF atomic write (creates .tmp)
// fails because creating a new file in a 0o555 dir requires EACCES.
func TestPersist_WritePDFError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based write-failure tests cannot run as root")
	}

	root := t.TempDir()
	t.Setenv("UPLOADS_ROOT", root)

	id := int64(7777)
	appDir := filepath.Join(root, "go-job", "applications", strconv.FormatInt(id, 10))

	// Pre-create the directory and MD files while the dir is writable.
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatalf("setup MkdirAll: %v", err)
	}
	for _, fname := range []string{"resume.md", "cover.md"} {
		if err := os.WriteFile(filepath.Join(appDir, fname), []byte("pre"), 0o644); err != nil {
			t.Fatalf("setup pre-write %s: %v", fname, err)
		}
	}

	// Restore dir permissions before cleanup so t.TempDir() can remove the files.
	t.Cleanup(func() { _ = os.Chmod(appDir, 0o755) }) //nolint:errcheck

	// Remove dir write permission. writePDF tries to create resume.pdf.tmp (a NEW
	// file) in this directory → EACCES. writeMD truncates existing files → succeeds
	// because that needs FILE write permission (0o644), not dir write permission.
	if err := os.Chmod(appDir, 0o555); err != nil {
		t.Fatalf("setup chmod 0o555: %v", err)
	}

	renderer := &stubRenderer{pdf: []byte("%PDF-1.4 stub")}
	auth := applications.New(renderer, "")

	beforeWrite := persistCounter(t, "error_pdf_write")
	beforeMdOnly := persistCounter(t, "ok_md_only")

	res, err := auth.Persist(context.Background(), id, "# Resume", "# Cover")

	if err != nil {
		t.Errorf("Persist must not return error for PDF write failure: %v", err)
	}
	if res.PDFRendered {
		t.Error("PDFRendered must be false when writePDF fails")
	}

	afterWrite := persistCounter(t, "error_pdf_write")
	afterMdOnly := persistCounter(t, "ok_md_only")

	if afterWrite-beforeWrite < 1 {
		t.Errorf("error_pdf_write counter: want ≥+1, got delta=%.0f", afterWrite-beforeWrite)
	}
	// ok_md_only must NOT be incremented — write failure ≠ binary-absent soft-skip.
	if afterMdOnly != beforeMdOnly {
		t.Errorf("ok_md_only must not increment on write failure: delta=%.0f", afterMdOnly-beforeMdOnly)
	}
}

// TestErrNoBinary_IsWrapped verifies that errors.Is(wrapped, ErrNoBinary)
// works when the sentinel is wrapped with fmt.Errorf %w — the pattern the
// pdfrender adapter uses when returning "binary not found" errors.
func TestErrNoBinary_IsWrapped(t *testing.T) {
	wrapped := fmt.Errorf("pdfrender: %w", applications.ErrNoBinary)
	if !errors.Is(wrapped, applications.ErrNoBinary) {
		t.Error("errors.Is must detect ErrNoBinary through fmt.Errorf wrapping")
	}
}
