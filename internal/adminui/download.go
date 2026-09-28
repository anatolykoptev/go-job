package adminui

import (
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/anatolykoptev/go-kit/uploads"
	"github.com/anatolykoptev/go_job/internal/engine/jobs/applications"
	"github.com/jackc/pgx/v5/pgxpool"
)

// validDownloadKinds is the allowlist for the {kind} path segment.
var validDownloadKinds = map[string]bool{
	applications.KindResume: true,
	applications.KindCover:  true,
}

// downloadHandler returns an http.HandlerFunc that serves resume/cover PDFs.
// GET /admin/jobs/{id}/download/{kind}
// Wrap with a.Require() before mounting on the mux.
//
// Resolution order (all under the ACTING account, plan ADR-11):
//  1. uploads-first: canonical $UPLOADS_ROOT/go-job/applications/<account>/<id>/<kind>.pdf
//     (+ the pre-P4 account-less location when the acting account is the operator)
//  2. legacy fallback: fuzzy slug match under APPLICATIONS_DIR — operator
//     account only; every other account gets a bare 404 (cross-account fetch
//     with a known path/job id is denied by construction).
func downloadHandler(pool *pgxpool.Pool, authority *applications.Authority, acctOf accountResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// No account identity → the download is not found. Fail-closed: a
		// missing/malformed session must not fall back to the operator's tree.
		aid, acctOK := acctOf(r.Context())
		if !acctOK || authority == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		acct := authority.ForAccount(aid)

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

		// 1. Uploads-first: direct stat on the account-scoped canonical path
		//    (operator also reads the pre-P4 account-less location).
		if pdfPath, ok := acct.Resolve(id64, kind); ok {
			uploadsRoot := uploads.Root()
			if !ValidatePathUnderRoot(uploadsRoot, pdfPath) {
				slog.Error("downloadHandler: uploads path traversal", "path", pdfPath, "root", uploadsRoot)
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/pdf")
			w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, filepath.Base(pdfPath)))
			http.ServeFile(w, r, pdfPath)
			return
		}

		// 2. Legacy fallback (operator only — enforced inside LegacyResolve):
		//    load company+title from DB then fuzzy-match slug.
		if authority.LegacyDir() == "" {
			http.Error(w, kind+" PDF not found", http.StatusNotFound)
			return
		}

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

		legacyPath := acct.LegacyResolve(company, title, kind)
		if legacyPath == "" {
			http.Error(w, "no prepared application for this job", http.StatusNotFound)
			return
		}

		legacyDir := authority.LegacyDir()
		if !ValidatePathUnderRoot(legacyDir, legacyPath) {
			slog.Error("downloadHandler: legacy path traversal", "path", legacyPath, "root", legacyDir)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		abs, err := filepath.Abs(legacyPath)
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
