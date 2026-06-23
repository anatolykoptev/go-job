// Package huntworker provides a durable scheduled ATS ingest worker.
//
// The worker fires periodically, calls existing ATS search functions
// (SearchGreenhouseJobs/Lever/Ashby) with generic role-query strings,
// and upserts results into hunt_jobs via UpsertJob.
//
// Separate from package hunt to avoid the import cycle:
//   engine/jobs → hunt (hunt_map.go) would cycle if hunt imported engine/jobs.
// huntworker imports both hunt and engine/jobs without any back-edge.
//
// Gate: HUNT_INGEST_ENABLED=true (default false).
// Interval: HUNT_INGEST_INTERVAL (default 6h).
// Queries: HUNT_INGEST_QUERIES comma-separated generic role strings
//          (default "software engineer,backend engineer,golang developer").
//          NO company names, NO ATS slugs — this is a PUBLIC repo.
package huntworker

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/anatolykoptev/go-kit/env"
	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/anatolykoptev/go_job/internal/hunt"
)

// defaultIngestQueries are generic role/skill strings used when the operator
// has not set HUNT_INGEST_QUERIES.  No company names, no personal targets —
// PUBLIC-repo-safe.
const defaultIngestQueries = "software engineer,backend engineer,golang developer"

// perPlatformTimeout caps one ATS search call (slug discovery + API fetch).
const perPlatformTimeout = 45 * time.Second

// Worker runs a periodic ATS ingest cycle.
type Worker struct {
	store    *hunt.Store
	interval time.Duration
	queries  []string
}

// NewWorker builds a Worker from env vars.  Returns nil if the store is nil
// (hunt DB not configured — silently disabled, matching the store-nil pattern
// everywhere in go-job).
func NewWorker(store *hunt.Store) *Worker {
	if store == nil {
		return nil
	}
	queries := parseQueries(env.Str("HUNT_INGEST_QUERIES", defaultIngestQueries))
	return &Worker{
		store:    store,
		interval: env.Duration("HUNT_INGEST_INTERVAL", 6*time.Hour),
		queries:  queries,
	}
}

// parseQueries splits a comma-separated query string, trims whitespace, and
// drops empty entries.
func parseQueries(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if q := strings.TrimSpace(p); q != "" {
			out = append(out, q)
		}
	}
	if len(out) == 0 {
		out = parseQueries(defaultIngestQueries)
	}
	return out
}

// Run blocks until ctx is cancelled, firing a cycle every interval.
// Each cycle recovers from panics so one bad platform cannot abort others.
// Intended to run as a goroutine in main.go.
func (w *Worker) Run(ctx context.Context) {
	slog.Info("hunt worker: starting",
		slog.Duration("interval", w.interval),
		slog.Int("queries", len(w.queries)),
	)

	// Run one cycle immediately so the table is populated before the first tick.
	w.runCycle(ctx)

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("hunt worker: stopping")
			return
		case <-ticker.C:
			w.runCycle(ctx)
		}
	}
}

// runCycle executes one ingest cycle across all configured role queries and
// the three ATS platforms (greenhouse, lever, ashby).
func (w *Worker) runCycle(ctx context.Context) {
	start := time.Now()
	slog.Info("hunt worker: cycle start", slog.Int("queries", len(w.queries)))

	platforms := []struct {
		name   string
		search func(ctx context.Context, query, loc string, limit int) ([]engine.SearxngResult, error)
	}{
		{engine.DiscoveryPlatformGreenhouse, jobs.SearchGreenhouseJobs},
		{engine.DiscoveryPlatformLever, jobs.SearchLeverJobs},
		{engine.DiscoveryPlatformAshby, jobs.SearchAshbyJobs},
	}

	var totalCreated, totalMerged, totalError int

	for _, q := range w.queries {
		for _, p := range platforms {
			func() {
				defer func() {
					if r := recover(); r != nil {
						slog.Error("hunt worker: recovered panic",
							slog.String("platform", p.name),
							slog.String("query", q),
							slog.Any("panic", r),
						)
					}
				}()

				platCtx, cancel := context.WithTimeout(ctx, perPlatformTimeout)
				defer cancel()

				results, err := p.search(platCtx, q, "", 10)

				if err != nil {
					slog.Warn("hunt worker: platform error",
						slog.String("platform", p.name),
						slog.String("query", q),
						slog.Any("error", err),
					)
					return
				}

				for _, r := range results {
					if r.URL == "" {
						continue
					}
					j := jobs.SearxngResultToHuntJob(r, p.name)
					_, outcome, uErr := w.store.UpsertJob(ctx, j)
					engine.IncrHuntIngest(hunt.KindJob, outcome.String())
					switch {
					case uErr != nil:
						totalError++
						slog.Warn("hunt worker: upsert failed",
							slog.String("url", r.URL),
							slog.Any("error", uErr),
						)
					case outcome == hunt.OutcomeCreated:
						totalCreated++
					case outcome == hunt.OutcomeMerged:
						totalMerged++
					}
				}
			}()
		}
	}

	elapsed := time.Since(start)
	engine.ObserveHuntCycleDuration(elapsed.Seconds())
	slog.Info("hunt worker: cycle complete",
		slog.Duration("elapsed", elapsed),
		slog.Int("created", totalCreated),
		slog.Int("merged", totalMerged),
		slog.Int("errors", totalError),
	)
}

// huntIngestEnabled reads the HUNT_INGEST_ENABLED env flag.
func huntIngestEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("HUNT_INGEST_ENABLED")), "true")
}

// StartWorker starts the durable ingest worker in a background goroutine when
// HUNT_INGEST_ENABLED=true and the store is available.  Noop otherwise.
// Must be called after engine.SetHuntStore.
func StartWorker(ctx context.Context, store *hunt.Store) {
	if !huntIngestEnabled() {
		slog.Debug("hunt worker: disabled (HUNT_INGEST_ENABLED not set)")
		return
	}
	w := NewWorker(store)
	if w == nil {
		slog.Warn("hunt worker: no store — skipping")
		return
	}
	go w.Run(ctx)
}
