// migrate-tracker-json — one-shot migration of _tracker.json favorites into
// hunt_jobs + hunt_ratings.
//
// Usage:
//
//	migrate-tracker-json [-file PATH] [-dsn DSN] [-user USERNAME] [-dry-run]
//
// It reads the JSON file, upserts each entry into hunt_jobs (dedup by URL via
// DedupHash) and inserts/updates a hunt_ratings row (stage=saved) for the given
// user. The stale 0–16 score field is NEVER written to fit_score — the LLM
// scorer owns that column.
//
// The command is idempotent: re-running it produces 0 new rows (ON CONFLICT).
// Use -dry-run to see what would be inserted without touching the DB.
//
// Status mapping (both → stage=saved; pack-ready is a derived PDF badge, not a stage):
//
//	saved      → saved
//	pack-ready → saved
//
// Comp string parsing (best-effort, "$190K – $270K • Offers Equity" → salary_min/max):
// Unparseable strings are stashed in the hunt_ratings note.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/jackc/pgx/v5/pgxpool"
)

// trackerFile is the top-level shape of _tracker.json.
type trackerFile struct {
	Version int          `json:"version"`
	Updated string       `json:"updated"`
	Jobs    []trackerJob `json:"jobs"`
}

// trackerJob is one entry in _tracker.json.jobs.
type trackerJob struct {
	Score      int    `json:"score"`      // stale 0–16 — NEVER written to fit_score
	Company    string `json:"company"`
	Title      string `json:"title"`
	Location   string `json:"location"`
	URL        string `json:"url"`
	Comp       string `json:"comp"`       // free-text salary, e.g. "$190K – $270K • Offers Equity"
	Department string `json:"department"`
	Team       string `json:"team"`
	Status     string `json:"status"` // "saved" | "pack-ready"
	Added      string `json:"added"`  // "YYYY-MM-DD"
}

// mapStatus maps _tracker.json status to hunt_ratings stage.
// pack-ready is a filesystem-derived badge, not a tracking stage.
// Both "saved" and "pack-ready" map to "saved" so they appear on /admin/shortlist.
func mapStatus(status string) string {
	switch status {
	case hunt.StageSaved, "pack-ready":
		return hunt.StageSaved
	default:
		// Unknown status → treat as saved (fail-open, better than dropping the entry).
		return hunt.StageSaved
	}
}

// currencyUSD and intervalYear are the salary defaults for USD job postings.
const (
	currencyUSD  = "USD"
	intervalYear = "year"
)

// salaryRe matches patterns like "$190K", "$1.9M", "190,000" optionally followed
// by a separator and a max value. Handles K/M suffixes, dollar sign, commas.
var salaryRe = regexp.MustCompile(`\$?([\d,]+(?:\.\d+)?)\s*([KkMm]?)`)

// parseSalary parses a comp string into (min, max, currency, interval).
// Returns zeros/empty strings when the format is not recognised.
// Recognised formats:
//
//	"$190K – $270K • Offers Equity"  → 190000, 270000, "USD", "year"
//	"$190,000"                         → 190000, 0,      "USD", "year"
//	"190K-270K"                        → 190000, 270000, "USD", "year"
func parseSalary(comp string) (min, max int, currency, interval string) {
	if comp == "" {
		return
	}
	hasDollar := strings.Contains(comp, "$")
	matches := salaryRe.FindAllStringSubmatch(comp, -1)
	if len(matches) == 0 {
		return
	}
	parse := func(digits, suffix string) int {
		digits = strings.ReplaceAll(digits, ",", "")
		v, err := strconv.ParseFloat(digits, 64)
		if err != nil {
			return 0
		}
		switch strings.ToUpper(suffix) {
		case "K":
			v *= 1000
		case "M":
			v *= 1_000_000
		}
		return int(v)
	}
	min = parse(matches[0][1], matches[0][2])
	if len(matches) >= 2 {
		max = parse(matches[1][1], matches[1][2])
	}
	if min == 0 {
		return 0, 0, "", ""
	}
	if hasDollar {
		currency = currencyUSD
	}
	interval = intervalYear
	return
}

// buildNote constructs the hunt_ratings note from metadata we don't store as
// structured columns (department, team). Returns "" when all fields are empty.
func buildNote(j trackerJob) string {
	var parts []string
	if j.Department != "" {
		parts = append(parts, "dept:"+j.Department)
	}
	if j.Team != "" && j.Team != j.Department {
		parts = append(parts, "team:"+j.Team)
	}
	// Stash the comp string if salary parsing failed (so no data is silently lost).
	if j.Comp != "" {
		min, _, _, _ := parseSalary(j.Comp)
		if min == 0 {
			parts = append(parts, "comp:"+j.Comp)
		}
	}
	return strings.Join(parts, "; ")
}

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	filePath := flag.String("file", envOr("APPLICATIONS_DIR", "/data/applications")+"/_tracker.json", "path to _tracker.json")
	dsn := flag.String("dsn", os.Getenv("DATABASE_URL"), "postgres DSN (DATABASE_URL if unset)")
	user := flag.String("user", envOr("ADMIN_USERNAME", "admin"), "hunt_ratings user_name (matches ADMIN_USERNAME)")
	dryRun := flag.Bool("dry-run", false, "print what would be done without writing")
	flag.Parse()

	if *dsn == "" {
		return errors.New("DATABASE_URL or -dsn required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	raw, err := os.ReadFile(*filePath)
	if err != nil {
		return fmt.Errorf("read _tracker.json %s: %w", *filePath, err)
	}

	var tf trackerFile
	if err := json.Unmarshal(raw, &tf); err != nil {
		return fmt.Errorf("parse _tracker.json: %w", err)
	}

	slog.Info("loaded _tracker.json", "version", tf.Version, "updated", tf.Updated, "entries", len(tf.Jobs))

	if *dryRun {
		runDryRun(tf, *user)
		return nil
	}

	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		return fmt.Errorf("open pool: %w", err)
	}
	defer pool.Close()

	store := hunt.NewStore(pool)
	// Run schema migrations so the command is safe to run on a fresh DB.
	if err := store.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate schema: %w", err)
	}

	var nCreated, nMerged, nRated, nSkipped int
	for i, tj := range tf.Jobs {
		if tj.URL == "" {
			slog.Warn("skipping entry without URL", "index", i, "company", tj.Company, "title", tj.Title)
			nSkipped++
			continue
		}

		minSal, maxSal, currency, interval := parseSalary(tj.Comp)

		j := hunt.Job{
			DedupHash:      hunt.DedupHash(tj.URL),
			Title:          tj.Title,
			Company:        tj.Company,
			URL:            tj.URL,
			Source:         "tracker",
			Location:       parseLocation(tj.Location),
			Remote:         parseRemote(tj.Location),
			SalaryMin:      minSal,
			SalaryMax:      maxSal,
			SalaryCurrency: currency,
			SalaryInterval: interval,
			// fit_score NEVER written here — the LLM scorer owns that column.
			// score from _tracker.json is stale 0–16 and MUST NOT touch fit_score.
		}

		id, outcome, err := store.UpsertJob(ctx, j)
		if err != nil {
			slog.Error("upsert job", "company", tj.Company, "title", tj.Title, "err", err)
			nSkipped++
			continue
		}

		switch outcome {
		case hunt.OutcomeCreated:
			nCreated++
		case hunt.OutcomeMerged:
			nMerged++
		}

		stage := mapStatus(tj.Status)
		note := buildNote(tj)

		// Route to the correct axis after migration 012 split:
		// "saved" lives on triage; pipeline statuses live on stage.
		triage, stageVal := "", stage
		if stage == hunt.StageSaved {
			triage, stageVal = hunt.StageSaved, ""
		}
		if err := store.Rate(ctx, hunt.KindJob, id, *user, triage, stageVal, note); err != nil {
			slog.Error("rate job", "id", id, "company", tj.Company, "err", err)
			nSkipped++
			continue
		}
		nRated++

		slog.Info("migrated",
			"company", tj.Company,
			"title", tj.Title,
			"status", tj.Status,
			"stage", stage,
			"outcome", outcome,
		)
	}

	slog.Info("migration complete",
		"created", nCreated,
		"merged", nMerged,
		"rated", nRated,
		"skipped", nSkipped,
	)
	return nil
}

// runDryRun prints what the migration would do without touching the DB.
func runDryRun(tf trackerFile, user string) {
	fmt.Printf("DRY-RUN: _tracker.json v%d updated=%s entries=%d user=%s\n", tf.Version, tf.Updated, len(tf.Jobs), user)
	fmt.Printf("%-4s %-30s %-48s %-12s %-10s %s\n", "#", "Company", "Title", "Status→Stage", "Salary", "Note")
	fmt.Println(strings.Repeat("-", 130))
	for i, tj := range tf.Jobs {
		stage := mapStatus(tj.Status)
		minSal, maxSal, _, _ := parseSalary(tj.Comp)
		salStr := ""
		if minSal > 0 {
			salStr = fmt.Sprintf("%d-%d", minSal/1000, maxSal/1000) + "K"
		}
		note := buildNote(tj)
		fmt.Printf("%-4d %-30s %-48s %-12s %-10s %s\n",
			i+1,
			truncate(tj.Company, 29),
			truncate(tj.Title, 47),
			tj.Status+"→"+stage,
			salStr,
			note,
		)
	}
}

// parseLocation extracts a clean location string from the _tracker.json pipe-separated value.
// Example: "US Remote | REMOTE | Remote" → "US Remote".
func parseLocation(raw string) string {
	parts := strings.SplitN(raw, "|", 2)
	return strings.TrimSpace(parts[0])
}

// parseRemote checks whether the location string indicates a remote position.
func parseRemote(raw string) string {
	lower := strings.ToLower(raw)
	if strings.Contains(lower, "remote") {
		return "remote"
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
