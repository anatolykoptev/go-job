// Package huntworker provides a durable scheduled ATS ingest worker.
//
// The worker fires periodically, calls existing ATS search functions
// (SearchGreenhouseJobs/Lever/Ashby) with generic role-query strings,
// and upserts results into hunt_jobs via UpsertJob.
//
// Separate from package hunt to avoid the import cycle:
//
//	engine/jobs → hunt (hunt_map.go) would cycle if hunt imported engine/jobs.
//
// huntworker imports both hunt and engine/jobs without any back-edge.
// internal/hunt/score imports only hunt types (no engine), so the graph is:
//
//	huntworker → engine (CallLLM)
//	huntworker → engine/jobs (ScoreJobMatchCoverage, search functions)
//	huntworker → hunt (Store, Job, ScoreResult, Outcome)
//	huntworker → hunt/score (Score, ScorerDeps, ScoringProfile)
//	hunt/score → hunt (types only, no engine — cycle-free)
//
// Gate: HUNT_INGEST_ENABLED=true (default false).
// Interval: HUNT_INGEST_INTERVAL (default 6h).
// Queries: HUNT_INGEST_QUERIES comma-separated generic role strings
//
//	(default "software engineer,backend engineer,golang developer").
//	NO company names, NO ATS slugs — this is a PUBLIC repo.
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
	"github.com/anatolykoptev/go_job/internal/hunt/score"
)

// jobScoreSetter is the narrow store interface used by scoring helpers.
// *hunt.Store satisfies this; tests inject a fake.
type jobScoreSetter interface {
	SetJobScore(ctx context.Context, id int64, sr hunt.ScoreResult) error
}

// defaultIngestQueries are generic role/skill strings used when the operator
// has not set HUNT_INGEST_QUERIES.  No company names, no personal targets —
// PUBLIC-repo-safe.
const defaultIngestQueries = "software engineer,backend engineer,golang developer"

// perPlatformTimeout caps one ATS search call (slug discovery + API fetch).
const perPlatformTimeout = 45 * time.Second

// Worker runs a periodic ATS ingest cycle.
type Worker struct {
	store          *hunt.Store
	notifier       hunt.Notifier
	interval       time.Duration
	queries        []string
	scoringProfile *score.ScoringProfile // nil = scoring disabled
	scorerDeps     score.ScorerDeps
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
		// scoringProfile is loaded lazily on first Run (requires DB + context).
		// scorerDeps wired with concrete engine functions.
		scorerDeps: score.ScorerDeps{
			Jaccard: func(profileKW, jobText string) float64 {
				kw := jobs.ExtractResumeKeywords(profileKW)
				return jobs.ScoreJobMatchCoverage(kw, jobText)
			},
			LLM: engine.CallLLM,
		},
	}
}

// SetNotifier wires the Telegram notifier into the worker.
// Must be called before Run(). Optional — if nil, no notifications are sent.
func (w *Worker) SetNotifier(n hunt.Notifier) { w.notifier = n }

// maybeNotifyJob fires NotifyNewJob when the outcome is OutcomeCreated and the
// job is open or has an empty status. Empty status is treated as open because
// SearxngResultToHuntJob does not set a Status field — UpsertJob normalises it
// to StatusOpen in Postgres, but the in-memory Job struct retains "".
func (w *Worker) maybeNotifyJob(j hunt.Job, outcome hunt.Outcome) {
	if outcome == hunt.OutcomeCreated && w.notifier != nil &&
		(j.Status == hunt.StatusOpen || j.Status == "") {
		w.notifier.NotifyNewJob(j)
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

	// Load the scoring profile once at startup (requires context + DB).
	if score.ScoringEnabled() && w.store != nil {
		prof, err := score.LoadProfile(ctx, w.store.Pool())
		if err != nil {
			slog.WarnContext(ctx, "hunt worker: scoring profile load error — scoring disabled",
				slog.Any("error", err))
		} else {
			w.scoringProfile = prof // nil = disabled (LoadProfile logs its own WARN)
		}
	}

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
	llmCallsThisCycle := 0 // circuit-breaker counter, reset per cycle

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
					engine.IncrHuntPostedAt(p.name, j.PostedAt != nil)
					id, outcome, uErr := w.store.UpsertJob(ctx, j)
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
						// Attach the job ID so SetJobScore can find the row.
						j.ID = id
						// Score first (persist fit data), then notify — both fire on
						// OutcomeCreated; scoring is orthogonal to notification.
						scoreJobWithLimit(ctx, outcome, j,
							w.scoringProfile, w.scorerDeps, w.store, &llmCallsThisCycle)
						w.maybeNotifyJob(j, outcome)
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
		slog.Int("llm_scored", llmCallsThisCycle),
	)
}

// scoreJobWithLimit scores a job, enforcing the per-cycle LLM circuit breaker.
// If the circuit breaker has tripped (llmCallsThisCycle >= max), the job is
// persisted as "unscored" without calling the LLM.
//
// llmCallsThisCycle is incremented only when the LLM is actually called.
func scoreJobWithLimit(
	ctx context.Context,
	outcome hunt.Outcome,
	job hunt.Job,
	profile *score.ScoringProfile,
	deps score.ScorerDeps,
	store jobScoreSetter,
	llmCallsThisCycle *int,
) {
	if outcome != hunt.OutcomeCreated {
		return
	}

	maxLLM := score.MaxLLMPerCycle()
	if *llmCallsThisCycle >= maxLLM {
		// Circuit breaker tripped: persist unscored, do not call LLM.
		result := hunt.ScoreResult{FitBand: "unscored", ScoredAt: time.Now()}
		if err := store.SetJobScore(ctx, job.ID, result); err != nil {
			slog.WarnContext(ctx, "hunt worker: SetJobScore (circuit-breaker unscored) failed",
				slog.Int64("job_id", job.ID),
				slog.Any("error", err),
			)
		}
		return
	}

	// Run the full cascade scorer, then count this slot regardless of whether
	// the inner scorer skipped the LLM (stale/reject/nil-profile).  The circuit
	// breaker is a conservative per-cycle cap on *potential* LLM calls, not an
	// exact counter.  Overcount is intentional and safe.
	scoreJobIfCreated(ctx, outcome, job, profile, deps, store)
	*llmCallsThisCycle++
}

// scoreJobIfCreated scores a single OutcomeCreated job and persists the result.
// It is extracted as a separate function for unit testability (injected store + deps).
// No-op for any outcome other than OutcomeCreated.
//
// Write failures from SetJobScore are logged but do not abort the cycle.
func scoreJobIfCreated(
	ctx context.Context,
	outcome hunt.Outcome,
	job hunt.Job,
	profile *score.ScoringProfile,
	deps score.ScorerDeps,
	store jobScoreSetter,
) {
	if outcome != hunt.OutcomeCreated {
		return
	}

	result := score.Score(ctx, profile, job, deps)

	if err := store.SetJobScore(ctx, job.ID, result); err != nil {
		slog.WarnContext(ctx, "hunt worker: SetJobScore failed",
			slog.Int64("job_id", job.ID),
			slog.String("fit_band", result.FitBand),
			slog.Any("error", err),
		)
	}
}

// huntIngestEnabled reads the HUNT_INGEST_ENABLED env flag.
func huntIngestEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("HUNT_INGEST_ENABLED")), "true")
}

// StartWorker starts the durable ingest worker in a background goroutine when
// HUNT_INGEST_ENABLED=true and the store is available.  Noop otherwise.
// Must be called after engine.SetHuntStore.
// notifier may be nil — if nil, no Telegram notifications are sent by the worker.
func StartWorker(ctx context.Context, store *hunt.Store, notifier hunt.Notifier) {
	if !huntIngestEnabled() {
		slog.Debug("hunt worker: disabled (HUNT_INGEST_ENABLED not set)")
		return
	}
	w := NewWorker(store)
	if w == nil {
		slog.Warn("hunt worker: no store — skipping")
		return
	}
	w.SetNotifier(notifier)
	go w.Run(ctx)
}
