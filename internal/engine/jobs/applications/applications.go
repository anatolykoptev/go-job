// Package applications is the single authority for application artifact paths,
// persistence, and resolution in go-job.
//
// Path layout (canonical):
//
//	$UPLOADS_ROOT/go-job/applications/<hunt_jobs.id>/<kind>.pdf
//	$UPLOADS_ROOT/go-job/applications/<hunt_jobs.id>/<kind>.md
//	$UPLOADS_ROOT/go-job/applications/<hunt_jobs.id>/meta.json
//
// No other package in go-job hand-spells this tuple. Callers resolve via
// Resolve/Exists and write via Persist.
package applications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/anatolykoptev/go-kit/uploads"
)

// ErrNoBinary is returned by a Renderer when the required CLI binaries
// (typst/pandoc) are absent from PATH. Persist treats this as a soft skip
// (md-only fallback), not a hard failure.
//
// The adapter (pdfrender.TypstAdapter) is the sole constructor — it wraps the
// render/typst "binary not found" message so isNoBinary can use errors.Is
// instead of substring-matching wrapped stderr.
var ErrNoBinary = errors.New("pdf binary not found")

const (
	// KindResume is the canonical kind name for a resume PDF/md.
	KindResume = "resume"
	// KindCover is the canonical kind name for a cover-letter PDF/md.
	KindCover = "cover"
)

// Renderer is the port (consumer-side interface) for PDF generation.
//
// Defined here so engine/jobs/applications does NOT import go-kit/render/typst
// or os/exec — those live in the adapter injected at main.go.
// go list -deps ./internal/engine/jobs/... must show no os/exec.
type Renderer interface {
	// PDF converts markdown to PDF bytes. Returns ErrNoBinary (via errors.Is)
	// when the required CLI tools are absent — callers treat this as a soft skip.
	PDF(ctx context.Context, md string) ([]byte, error)
}

// Meta is persisted alongside the artifact for auditing and re-render.
type Meta struct {
	JobID       int64     `json:"job_id"`
	GeneratedAt time.Time `json:"generated_at"`
	Renderer    string    `json:"renderer"`
	PDFRendered bool      `json:"pdf_rendered"`
}

// Result is the outcome of a Persist call.
type Result struct {
	// ResumePath is the absolute path to resume.pdf, empty when PDF was skipped.
	ResumePath string
	// CoverPath is the absolute path to cover.pdf, empty when PDF was skipped.
	CoverPath string
	// PDFRendered reports whether at least one PDF was written.
	PDFRendered bool
}

// Authority is the single read/write/resolve authority for application artifacts.
// Construct via New; safe for concurrent use after construction.
type Authority struct {
	renderer  Renderer // nil → PDF steps are no-ops (graceful degrade)
	legacyDir string   // APPLICATIONS_DIR — fuzzy legacy lookup; may be empty
}

// New creates an Authority.
//   - renderer may be nil — PDF steps degrade to md-only.
//   - legacyDir is the APPLICATIONS_DIR path for the transition fallback; may be "".
func New(renderer Renderer, legacyDir string) *Authority {
	return &Authority{renderer: renderer, legacyDir: legacyDir}
}

// LegacyDir returns the legacy applications directory (APPLICATIONS_DIR).
// Used by adminui for the LinkedIn page which reads LINKEDIN-UPDATE.md from it.
func (a *Authority) LegacyDir() string { return a.legacyDir }

// ─── Path helpers (single spelling site) ─────────────────────────────────────

// Path returns the canonical uploads path for an application PDF.
// The ONLY spelling of the uploads tuple in go-job.
// Layout: $UPLOADS_ROOT/go-job/applications/<id>/<kind>.pdf
func Path(id int64, kind string) (string, error) {
	return uploads.Path("go-job", fmt.Sprintf("applications/%d", id), kind+".pdf")
}

// MDPath returns the canonical uploads path for the markdown source.
func MDPath(id int64, kind string) (string, error) {
	return uploads.Path("go-job", fmt.Sprintf("applications/%d", id), kind+".md")
}

// metaPath returns the canonical uploads path for meta.json.
func metaPath(id int64) (string, error) {
	return uploads.Path("go-job", fmt.Sprintf("applications/%d", id), "meta.json")
}

// ─── Resolution ───────────────────────────────────────────────────────────────

// canonicalPDFPath returns the canonical path for a PDF artifact WITHOUT
// creating any directories. Use this on read paths (Resolve/Exists) to avoid
// littering the uploads volume with empty per-job dirs for artifact-less jobs.
// Write paths (writePDF/writeMD/writeMeta) still go through uploads.Path which
// does MkdirAll — that is correct and intentional.
func canonicalPDFPath(id int64, kind string) string {
	return filepath.Join(uploads.Root(), "go-job", "applications", strconv.FormatInt(id, 10), kind+".pdf")
}

// Resolve returns (absPath, true) when the PDF exists in uploads, ("", false)
// otherwise. Uses a direct stat on the computed path — NO MkdirAll — so
// existence checks on artifact-less jobs do not create empty directories.
func (a *Authority) Resolve(id int64, kind string) (string, bool) {
	p := canonicalPDFPath(id, kind)
	if _, err := os.Stat(p); err == nil {
		return p, true
	}
	return "", false
}

// Exists reports whether a PDF exists in uploads for the given id + kind.
func (a *Authority) Exists(id int64, kind string) bool {
	_, ok := a.Resolve(id, kind)
	return ok
}

// LegacyResolve falls back to the fuzzy slug-based lookup under legacyDir.
// Returns "" when legacyDir is empty, the slug can't be found, or no PDF matched.
func (a *Authority) LegacyResolve(company, title, kind string) string {
	if a.legacyDir == "" {
		return ""
	}
	slug, err := findApplicationSlug(a.legacyDir, company, title)
	if err != nil {
		return ""
	}
	return findApplicationPDF(filepath.Join(a.legacyDir, slug), kind)
}

// LegacyEntries reads the legacy dir once — use the returned entries for the
// batch variant to avoid N+1 ReadDir calls across a list of rows.
func (a *Authority) LegacyEntries() []os.DirEntry {
	if a.legacyDir == "" {
		return nil
	}
	entries, _ := os.ReadDir(a.legacyDir)
	return entries
}

// LegacyExistsFromEntries checks for a legacy PDF given a pre-loaded dir snapshot.
func (a *Authority) LegacyExistsFromEntries(entries []os.DirEntry, company, title, kind string) bool {
	if len(entries) == 0 || a.legacyDir == "" {
		return false
	}
	slug, err := findApplicationSlugFromEntries(entries, company, title)
	if err != nil {
		return false
	}
	return findApplicationPDF(filepath.Join(a.legacyDir, slug), kind) != ""
}

// LegacyResolveFromEntries is the batch-friendly variant of LegacyResolve.
// It avoids N+1 os.ReadDir calls by reusing the pre-loaded entries snapshot.
// Use this in list/batch loops; LegacyResolve calls ReadDir every time.
func (a *Authority) LegacyResolveFromEntries(entries []os.DirEntry, company, title, kind string) string {
	if len(entries) == 0 || a.legacyDir == "" {
		return ""
	}
	slug, err := findApplicationSlugFromEntries(entries, company, title)
	if err != nil {
		return ""
	}
	return findApplicationPDF(filepath.Join(a.legacyDir, slug), kind)
}

// ─── Persistence ──────────────────────────────────────────────────────────────

// Persist writes resumeMD + coverMD to uploads (markdown always), renders PDFs
// (soft-skip when renderer is nil or binary absent), and writes meta.json.
// Never returns a hard error for PDF-specific failures — only md-write or disk
// errors propagate.
func (a *Authority) Persist(ctx context.Context, id int64, resumeMD, coverMD string) (Result, error) {
	var result Result
	start := time.Now()

	// 1. Write markdown sources (always — source of truth for re-render).
	if err := writeMD(id, KindResume, resumeMD); err != nil {
		appPersistTotal.WithLabelValues("error_md").Inc()
		return result, fmt.Errorf("applications.Persist: write resume md: %w", err)
	}
	if err := writeMD(id, KindCover, coverMD); err != nil {
		appPersistTotal.WithLabelValues("error_md").Inc()
		return result, fmt.Errorf("applications.Persist: write cover md: %w", err)
	}

	rendererName := "none"
	var pdfWriteErr bool

	// 2. Render + write PDFs (graceful degrade when renderer nil or binary absent).
	if a.renderer != nil {
		rendererName = "typst"
		resumePDF, rerr := a.renderPDF(ctx, id, KindResume, resumeMD)
		if rerr == nil && len(resumePDF) > 0 {
			if p, err := writePDF(id, KindResume, resumePDF); err == nil {
				result.ResumePath = p
				result.PDFRendered = true
			} else {
				// Rendered but write failed (disk/permission) — NOT the same as
				// "binary absent" (ok_md_only). Log as error, bump separate outcome.
				slog.Error("applications: writePDF failed",
					"id", id, "kind", KindResume, "err", err)
				pdfWriteErr = true
			}
		}
		coverPDF, cerr := a.renderPDF(ctx, id, KindCover, coverMD)
		if cerr == nil && len(coverPDF) > 0 {
			if p, err := writePDF(id, KindCover, coverPDF); err == nil {
				result.CoverPath = p
			} else {
				slog.Error("applications: writePDF failed",
					"id", id, "kind", KindCover, "err", err)
				pdfWriteErr = true
			}
		}
	}

	// 3. Write meta.json (best-effort; never fails the persist).
	if err := writeMeta(id, Meta{
		JobID:       id,
		GeneratedAt: start,
		Renderer:    rendererName,
		PDFRendered: result.PDFRendered,
	}); err != nil {
		slog.Warn("applications: writeMeta failed", "id", id, "err", err)
	}

	// Distinguish "renderer absent / no bytes" (ok_md_only) from
	// "PDF rendered but write failed" (error_pdf_write) — masking a disk/perm
	// error as ok_md_only hides real breakage.
	outcome := "ok_with_pdf"
	if !result.PDFRendered {
		if pdfWriteErr {
			outcome = "error_pdf_write"
		} else {
			outcome = "ok_md_only"
		}
	}
	appPersistTotal.WithLabelValues(outcome).Inc()
	return result, nil
}

// renderPDF calls the renderer and records RED metrics.
func (a *Authority) renderPDF(ctx context.Context, id int64, kind, md string) ([]byte, error) {
	t := time.Now()
	pdf, err := a.renderer.PDF(ctx, md)
	elapsed := time.Since(t).Seconds()
	if err != nil {
		if isNoBinary(err) {
			appRenderTotal.WithLabelValues(kind, "skipped_no_binary").Inc()
			slog.Warn("applications.Persist: PDF binary absent, md-only fallback",
				"id", id, "kind", kind)
		} else {
			appRenderTotal.WithLabelValues(kind, "error").Inc()
			slog.Error("applications.Persist: PDF render failed",
				"id", id, "kind", kind, "err", err)
		}
		return nil, err
	}
	appRenderTotal.WithLabelValues(kind, "ok").Inc()
	appRenderDuration.WithLabelValues(kind).Observe(elapsed)
	return pdf, nil
}

// isNoBinary reports whether err is (or wraps) ErrNoBinary.
// The adapter (pdfrender.TypstAdapter) constructs ErrNoBinary only at
// LookPath-miss sites, so this check never misclassifies a real typst
// compile failure (e.g. "file not found" for a missing image) as a
// binary-absent soft-skip.
func isNoBinary(err error) bool {
	return errors.Is(err, ErrNoBinary)
}

// ─── File writers ─────────────────────────────────────────────────────────────

func writeMD(id int64, kind, content string) error {
	p, err := MDPath(id, kind)
	if err != nil {
		return err
	}
	return os.WriteFile(p, []byte(content), 0o600)
}

// writePDF writes data atomically: write to a .tmp file then os.Rename so a
// concurrent download handler never sees a torn (partial) PDF.
func writePDF(id int64, kind string, data []byte) (string, error) {
	p, err := Path(id, kind)
	if err != nil {
		return "", err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp) //nolint:errcheck // best-effort cleanup
		return "", err
	}
	return p, nil
}

func writeMeta(id int64, m Meta) error {
	p, err := metaPath(id)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}
