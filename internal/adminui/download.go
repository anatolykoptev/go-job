package adminui

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5/pgxpool"
)

// validDownloadKinds is the allowlist for the {kind} path segment.
var validDownloadKinds = map[string]bool{
	"resume": true,
	"cover":  true,
}

// downloadHandler returns an http.HandlerFunc that serves resume/cover PDFs.
// GET /admin/jobs/{id}/download/{kind}
// Wrap with a.Require() before mounting on the mux.
func downloadHandler(pool *pgxpool.Pool, applicationsDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rawID := r.PathValue("id")
		id64, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil || id64 <= 0 {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}

		kind := r.PathValue("kind")
		if !validDownloadKinds[kind] {
			http.Error(w, fmt.Sprintf("invalid kind %q; must be resume or cover", kind), http.StatusBadRequest)
			return
		}

		// Load company + title to derive the slug directory.
		var company, title string
		row := pool.QueryRow(r.Context(),
			`SELECT COALESCE(company,''), COALESCE(title,'') FROM hunt_jobs WHERE id = $1`,
			id64)
		if err := row.Scan(&company, &title); err != nil {
			if isJobNotFound(err) {
				http.Error(w, "job not found", http.StatusNotFound)
				return
			}
			slog.Error("downloadHandler: query hunt_jobs", "id", id64, "err", err)
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}

		slug, err := findApplicationSlug(applicationsDir, company, title)
		if err != nil {
			http.Error(w, "no prepared application for this job", http.StatusNotFound)
			return
		}

		slugDir := filepath.Join(applicationsDir, slug)
		pdfPath := findApplicationPDF(slugDir, kind)
		if pdfPath == "" {
			http.Error(w, kind+" PDF not found", http.StatusNotFound)
			return
		}

		if !ValidatePathUnderRoot(applicationsDir, pdfPath) {
			slog.Error("downloadHandler: path traversal detected", "path", pdfPath, "root", applicationsDir)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		abs, err := filepath.Abs(pdfPath)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, filepath.Base(abs)))
		http.ServeFile(w, r, abs)
	}
}

// ValidatePathUnderRoot reports whether filePath is safely contained under root
// after resolving symlinks. Returns false when filePath escapes root (forbidden).
// Exported for testing the symlink-escape guard without a live DB.
//
// Ported verbatim from go-nerv/internal/admin/pathutil.go.
func ValidatePathUnderRoot(root, filePath string) bool {
	abs, err := filepath.Abs(filePath)
	if err != nil {
		return false
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// Dangling symlink or missing file — treat as not-found (not forbidden,
		// but not safe to serve). Caller can distinguish by error; here we return
		// false to signal "do not serve".
		return false
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return false
	}
	return strings.HasPrefix(resolved, rootResolved+string(filepath.Separator))
}

// findApplicationSlug scans applicationsDir to find a slug directory whose
// name matches company+title after normalisation. Returns an error when none found.
//
// Ported from go-nerv/internal/admin/page_hunt.go.
func findApplicationSlug(applicationsDir, company, title string) (string, error) {
	entries, err := os.ReadDir(applicationsDir)
	if err != nil {
		return "", fmt.Errorf("read applications dir: %w", err)
	}
	return findApplicationSlugFromEntries(entries, company, title)
}

// findApplicationSlugFromEntries is the testable inner of findApplicationSlug.
//
// Ported from go-nerv/internal/admin/page_hunt.go.
func findApplicationSlugFromEntries(entries []os.DirEntry, company, title string) (string, error) {
	companySlug := slugify(company)
	if companySlug == "" {
		return "", fmt.Errorf("empty company slug for title %q", title)
	}
	// An application dir name is a curated TOKEN SUBSET of company+title (the
	// operator drops the less-distinctive title words, e.g. "anthropic-databases").
	// Match anchored on the company slug: every token in the dir name must appear
	// in the job's company+title token set. The most-specific dir wins (most
	// tokens, then longest name); a tie between two distinct dirs is ambiguous and
	// rejected (better no link than the wrong job's PDF). A naive full-title
	// HasPrefix would 404 on these abbreviated dirs.
	jobTokens := make(map[string]bool)
	for _, tok := range strings.Split(slugify(company+"-"+title), "-") {
		if tok != "" {
			jobTokens[tok] = true
		}
	}
	prefix := companySlug + "-"
	bestSlug, bestScore, bestLen := "", 0, 0
	ambiguous := false
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		matched, allMatch := 0, true
		for _, tok := range strings.Split(name, "-") {
			if tok == "" {
				continue
			}
			if jobTokens[tok] {
				matched++
			} else {
				allMatch = false
				break
			}
		}
		if !allMatch {
			continue
		}
		switch {
		case matched > bestScore || (matched == bestScore && len(name) > bestLen):
			bestSlug, bestScore, bestLen, ambiguous = name, matched, len(name), false
		case matched == bestScore && len(name) == bestLen && name != bestSlug:
			ambiguous = true
		}
	}
	if bestSlug == "" {
		return "", fmt.Errorf("no application slug matching company=%q title=%q", company, title)
	}
	if ambiguous {
		return "", fmt.Errorf("ambiguous application dir for company=%q title=%q", company, title)
	}
	return bestSlug, nil
}

// scanJobPDFs reports whether a prepared resume / cover PDF exists for the job's
// application directory (derived from company+title). Used to gate the detail-page
// download links so they appear only when the file actually exists (no dead 404s).
func scanJobPDFs(applicationsDir, company, title string) (hasResume, hasCover bool) {
	if applicationsDir == "" {
		return false, false
	}
	slug, err := findApplicationSlug(applicationsDir, company, title)
	if err != nil {
		return false, false
	}
	slugDir := filepath.Join(applicationsDir, slug)
	return findApplicationPDF(slugDir, "resume") != "", findApplicationPDF(slugDir, "cover") != ""
}

// slugNonAlphanumRe matches any run of non-alphanumeric characters.
var slugNonAlphanumRe = regexp.MustCompile(`[^a-z0-9]+`)

// slugify converts a string to a lower-case slug.
//
// Ported from go-nerv/internal/admin/page_hunt.go.
func slugify(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return slugNonAlphanumRe.ReplaceAllString(b.String(), "-")
}

// findApplicationPDF returns the path of the first matching PDF for kind in slugDir,
// using a 2-stage lookup:
//  1. <slugDir>/submit/<kind>*.pdf  — canonical location (resume-coach subagents)
//  2. <slugDir>/<kind>*.pdf         — legacy slug-root fallback (warns via slog)
//
// Returns "" when no PDF is found in either location.
//
// Ported verbatim from go-nerv/internal/admin/page_hunt.go.
func findApplicationPDF(slugDir, kind string) string {
	// Stage 1: canonical submit/ location.
	submitMatches, _ := filepath.Glob(filepath.Join(slugDir, "submit", kind+"*.pdf"))
	if len(submitMatches) > 0 {
		return submitMatches[0]
	}

	// Stage 2: legacy slug-root fallback.
	rootMatches, _ := filepath.Glob(filepath.Join(slugDir, kind+"*.pdf"))
	if len(rootMatches) > 0 {
		slog.Warn("findApplicationPDF: serving PDF from legacy slug root; move to submit/",
			"slugDir", slugDir, "kind", kind, "file", filepath.Base(rootMatches[0]))
		return rootMatches[0]
	}

	return ""
}
