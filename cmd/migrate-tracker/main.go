// cmd/migrate-tracker migrates the legacy SQLite tracker.db rows into postgres
// hunt_jobs + hunt_ratings. Idempotent — safe to re-run.
// Usage: go run ./cmd/migrate-tracker -db <path> -dsn <DATABASE_URL>
//        go run ./cmd/migrate-tracker -dry-run -db <path> -dsn <DATABASE_URL>
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "modernc.org/sqlite"
)

type sqliteJob struct {
	id        int64
	title     string
	company   string
	url       string
	status    string
	notes     string
	salary    string
	location  string
	createdAt string
	updatedAt string
}

func main() {
	dbPath := flag.String("db", "", "Path to SQLite tracker.db (required)")
	dsn := flag.String("dsn", os.Getenv("DATABASE_URL"), "Postgres DSN (defaults to $DATABASE_URL)")
	dryRun := flag.Bool("dry-run", false, "Print what would be migrated without writing")
	flag.Parse()

	if *dbPath == "" {
		slog.Error("--db is required")
		os.Exit(1)
	}
	if *dsn == "" {
		slog.Error("--dsn or $DATABASE_URL is required")
		os.Exit(1)
	}

	if err := run(context.Background(), *dbPath, *dsn, *dryRun); err != nil {
		slog.Error("migration failed", "err", err)
		os.Exit(1)
	}
}

// run is extracted so defer statements execute on all exit paths.
// main() calls os.Exit based on the returned error.
func run(ctx context.Context, dbPath, dsn string, dryRun bool) error {
	// Open SQLite
	sqliteDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("open sqlite: %w", err)
	}
	defer sqliteDB.Close() //nolint:errcheck

	rows, err := sqliteDB.QueryContext(ctx,
		"SELECT id, title, company, COALESCE(url,''), COALESCE(status,'saved'), COALESCE(notes,''), COALESCE(salary,''), COALESCE(location,''), created_at, updated_at FROM tracked_jobs ORDER BY id")
	if err != nil {
		return fmt.Errorf("query sqlite: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var jobs []sqliteJob
	for rows.Next() {
		var j sqliteJob
		if scanErr := rows.Scan(&j.id, &j.title, &j.company, &j.url, &j.status, &j.notes, &j.salary, &j.location, &j.createdAt, &j.updatedAt); scanErr != nil {
			return fmt.Errorf("scan row: %w", scanErr)
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rows err: %w", err)
	}

	slog.Info("found SQLite rows", "count", len(jobs))

	if dryRun {
		for _, j := range jobs {
			fmt.Printf("[dry-run] id=%d title=%q company=%q url=%q status=%q\n",
				j.id, j.title, j.company, j.url, j.status)
		}
		return nil
	}

	// Open postgres
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer pool.Close()

	store := hunt.NewStore(pool)

	start := time.Now()
	migrated, skipped := 0, 0
	for _, j := range jobs {
		dedupHash := ""
		if j.url != "" {
			dedupHash = hunt.DedupHash(j.url)
		} else {
			slug := strings.ToLower(strings.ReplaceAll(j.company+"-"+j.title, " ", "-"))
			dedupHash = hunt.DedupHash(slug)
		}

		hj := hunt.Job{
			DedupHash: dedupHash,
			Title:     j.title,
			Company:   j.company,
			URL:       j.url,
			Source:    "tracker-migration",
			Location:  j.location,
		}

		id, _, err := store.UpsertJob(ctx, hj)
		if err != nil {
			slog.Error("upsert job", "title", j.title, "err", err)
			skipped++
			continue
		}

		note := j.notes
		if j.salary != "" {
			if note != "" {
				note += "; salary: " + j.salary
			} else {
				note = "salary: " + j.salary
			}
		}

		stage := strings.ToLower(j.status)
		if stage == "" {
			stage = "saved"
		}

		// Route to the correct axis after migration 012 split.
		triage, stageVal := "", stage
		if stage == hunt.StageSaved {
			triage, stageVal = hunt.StageSaved, ""
		}
		if err := store.Rate(ctx, hunt.KindJob, id, "krolik", triage, stageVal, note); err != nil {
			slog.Error("rate job", "id", id, "err", err)
			skipped++
			continue
		}

		slog.Info("migrated", "sqlite_id", j.id, "pg_id", id, "title", j.title, "status", stage)
		migrated++
	}

	slog.Info("migration complete",
		"migrated", migrated,
		"skipped", skipped,
		"total", len(jobs),
		"elapsed", time.Since(start))
	return nil
}
