// cmd/migrate-application-pdfs is a one-shot migration that copies
// application PDFs from the legacy slug-based directory (APPLICATIONS_DIR)
// into the canonical uploads layout (go-kit/uploads, keyed by hunt_jobs.id).
//
// Usage:
//
//	APPLICATIONS_DIR=/data/applications \
//	DATABASE_URL=postgres://... \
//	    go run ./cmd/migrate-application-pdfs [--dry-run]
//
// The migrator:
//  1. Reads every hunt_jobs row (id, company, title).
//  2. For each job: fuzzy-matches a slug dir under APPLICATIONS_DIR.
//  3. For each of {resume, cover}: copies the found PDF to the uploads path
//     when the destination does not already exist (idempotent).
//  4. Logs every outcome and exits non-zero on any hard error.
//
// Run once after an operator deploy that includes go-kit/uploads wiring.
// Re-running is safe — skip_exists prevents double-copy.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/anatolykoptev/go_job/internal/engine/jobs/applications"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	dryRun := flag.Bool("dry-run", false, "print planned copies without writing")
	flag.Parse()

	if err := run(context.Background(), *dryRun); err != nil {
		slog.Error("migration failed", "err", err)
		os.Exit(1)
	}
}

// run is extracted so defer statements execute on all exit paths.
// main() calls os.Exit based on the returned error.
func run(ctx context.Context, dryRun bool) error {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return errors.New("DATABASE_URL not set")
	}
	legacyDir := os.Getenv("APPLICATIONS_DIR")
	if legacyDir == "" {
		return errors.New("APPLICATIONS_DIR not set")
	}

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("connect db: %w", err)
	}
	defer pool.Close()

	// authority without a renderer — used only for path resolution.
	auth := applications.New(nil, legacyDir)
	legacyEntries := auth.LegacyEntries()
	if len(legacyEntries) == 0 {
		slog.Warn("no legacy entries found; APPLICATIONS_DIR may be empty or missing", "dir", legacyDir)
	}

	rows, err := pool.Query(ctx,
		`SELECT id, COALESCE(company,''), COALESCE(title,'') FROM hunt_jobs ORDER BY id`)
	if err != nil {
		return fmt.Errorf("query hunt_jobs: %w", err)
	}
	defer rows.Close()

	var ok, skipped, unmatched, errCount int

	for rows.Next() {
		var id int64
		var company, title string
		if scanErr := rows.Scan(&id, &company, &title); scanErr != nil {
			slog.Error("scan row", "err", scanErr)
			errCount++
			continue
		}
		for _, kind := range []string{applications.KindResume, applications.KindCover} {
			// Use the pre-loaded entries snapshot — avoids N+1 os.ReadDir calls.
			src := auth.LegacyResolveFromEntries(legacyEntries, company, title, kind)
			if src == "" {
				unmatched++
				slog.Debug("no legacy PDF", "id", id, "kind", kind, "company", company)
				continue
			}

			dst, pathErr := applications.Path(id, kind)
			if pathErr != nil {
				slog.Error("canonical path", "id", id, "kind", kind, "err", pathErr)
				errCount++
				continue
			}

			// Idempotent: skip when destination already exists.
			if _, statErr := os.Stat(dst); statErr == nil {
				skipped++
				slog.Debug("skip_exists", "id", id, "kind", kind, "dst", dst)
				continue
			}

			if dryRun {
				slog.Info("[dry-run] would copy", "src", src, "dst", dst)
				ok++
				continue
			}

			if copyErr := copyFile(src, dst); copyErr != nil {
				slog.Error("copy", "src", src, "dst", dst, "err", copyErr)
				errCount++
				continue
			}
			ok++
			slog.Info("migrated", "id", id, "kind", kind, "src", src, "dst", dst)
		}
	}
	if rows.Err() != nil {
		return fmt.Errorf("rows.Err: %w", rows.Err())
	}

	slog.Info("migration complete",
		"ok", ok, "skipped", skipped, "unmatched", unmatched, "errors", errCount)
	if errCount > 0 {
		return fmt.Errorf("%d copy error(s) — see logs above", errCount)
	}
	return nil
}

// copyFile copies src to dst atomically via a temp file in the same dir.
func copyFile(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // operator-controlled paths from DB
	if err != nil {
		return err
	}
	defer in.Close() //nolint:errcheck

	// Write to a temp file next to dst, then rename (atomic on Linux).
	tmp := dst + ".tmp"
	// 0644: world-readable so a non-root / cap-dropped container process can
	// read migrated PDFs. The uploads volume is private + downloads are behind
	// admin auth, so 0644 is acceptable.
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644) //nolint:gosec
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()    //nolint:errcheck
		os.Remove(tmp) //nolint:errcheck
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp) //nolint:errcheck
		return err
	}
	return os.Rename(tmp, dst)
}
