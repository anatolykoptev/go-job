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
	"strconv"
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

// unscoredJobStore is the narrow store interface used by the end-of-cycle
// unscored-open sweep. Implemented by *hunt.Store; tests inject a fake.
type unscoredJobStore interface {
	jobScoreSetter
	UnscoredOpenJobs(ctx context.Context, limit int, rescoreAll bool) ([]hunt.Job, error)
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
	notifyMetric   func(outcome string) // wired to engine.IncrHuntNotify in production
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
		store:        store,
		interval:     env.Duration("HUNT_INGEST_INTERVAL", 6*time.Hour),
		queries:      queries,
		notifyMetric: engine.IncrHuntNotify,
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

// huntNotifyMinFit reads HUNT_NOTIFY_MIN_FIT from the environment.
// Default 0 = gate fully open (all scored jobs pass through).
// Set to e.g. 60 to drop jobs with fit_score < 60.
//
// Read PER CALL (not snapshotted at NewWorker) BY DESIGN: Phase 7 flips the gate
// by raising this env var, and reading it each cycle lets the change take effect
// without a redeploy (per the migration plan's "no deploy if read each cycle").
// A non-numeric or negative value clamps to 0 (gate open) — a malformed knob must
// never silently start dropping jobs.
func huntNotifyMinFit() int {
	v := strings.TrimSpace(os.Getenv("HUNT_NOTIFY_MIN_FIT"))
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// maybeNotifyJob applies the fit gate and fires NotifyNewJob when the outcome
// is OutcomeCreated and the job is open (or has empty status — see comment
// on SearxngResultToHuntJob below).
//
// Gate table (Phase 5):
//   - score == nil (scoring disabled)        → notify (recency-only card), terminal outcome via notifier
//   - score.FitBand == "unscored" (LLM fail) → notify (degraded card); notifier emits "unscored" post-recency
//   - score.FitScore < MIN_FIT (real band)   → skip notify, metric "low_fit" (emitted HERE, terminal)
//   - else                                    → notify (full fit-card), terminal outcome via notifier
//
// Metric ownership (no double-count): the ONLY outcome this method emits is
// "low_fit" — and only on the terminal-drop path that returns without dispatch.
// All other outcomes (sent/failed/stale/no_date/unscored) are emitted by the
// notifier AFTER its recency gate, so a stale unscored job counts once ("stale"),
// not twice. See ProductNotifier.NotifyNewJob.
//
// Empty status is treated as open because SearxngResultToHuntJob does not set a
// Status field — UpsertJob normalises it to StatusOpen in Postgres, but the
// in-memory Job struct retains "".
func (w *Worker) maybeNotifyJob(j hunt.Job, outcome hunt.Outcome, score *hunt.ScoreResult) {
	if outcome != hunt.OutcomeCreated {
		return
	}
	if w.notifier == nil {
		return
	}
	if j.Status != hunt.StatusOpen && j.Status != "" {
		return
	}

	// Fit gate — only a REAL score (not nil, not "unscored") is gated. A
	// sub-threshold real score is a TERMINAL drop: the job is not dispatched and
	// "low_fit" is emitted here exactly once (mutually exclusive with the
	// notifier's stale/no_date/sent outcomes because we return).
	if score != nil && score.FitBand != hunt.FitBandUnscored {
		minFit := huntNotifyMinFit()
		if minFit > 0 && score.FitScore < minFit {
			if w.notifyMetric != nil {
				w.notifyMetric("low_fit")
			}
			slog.Debug("hunt worker: fit gate dropped job",
				slog.Int64("job_id", j.ID),
				slog.Int("fit_score", score.FitScore),
				slog.Int("min_fit", minFit),
			)
			return
		}
	}

	// nil score (scoring disabled), unscored (LLM-fail fail-open), or
	// fit ≥ threshold: dispatch. The notifier owns the recency gate and emits the
	// terminal outcome (sent/failed/stale/no_date); for a degraded (unscored)
	// card it emits "unscored" AFTER recency passes — so a stale unscored job
	// counts only as "stale", never "unscored"+"stale". The fit gate above is the
	// ONLY metric this worker emits pre-dispatch.
	w.notifier.NotifyNewJob(j, score)
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
						sr := scoreJobWithLimit(ctx, outcome, j,
							w.scoringProfile, w.scorerDeps, w.store, &llmCallsThisCycle)
						if sr != nil {
							observeScore(*sr)
						}
						w.maybeNotifyJob(j, outcome, sr)
					case outcome == hunt.OutcomeMerged:
						totalMerged++
					}
				}
			}()
		}
	}

	// End-of-cycle unscored-open sweep: score jobs that were ingested in previous
	// cycles but never scored (scored_at IS NULL). Only when scoring is enabled and
	// the store satisfies the unscoredJobStore interface (production path).
	if w.scoringProfile != nil {
		if sweepStore, ok := interface{}(w.store).(unscoredJobStore); ok {
			runUnscoredSweep(ctx, sweepStore, w.scoringProfile, w.scorerDeps, &llmCallsThisCycle)
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
// Returns a pointer to the ScoreResult so the caller (runCycle) can thread it
// into maybeNotifyJob for the fit gate and card rendering. Returns nil when
// outcome is not OutcomeCreated (no scoring performed).
//
// llmCallsThisCycle is incremented ONLY when the LLM was actually invoked
// (ScoreResult.LLMCalled == true). Stale, sub-Jaccard, and nil-profile jobs
// short-circuit before the LLM and must NOT consume budget.
func scoreJobWithLimit(
	ctx context.Context,
	outcome hunt.Outcome,
	job hunt.Job,
	profile *score.ScoringProfile,
	deps score.ScorerDeps,
	store jobScoreSetter,
	llmCallsThisCycle *int,
) *hunt.ScoreResult {
	if outcome != hunt.OutcomeCreated {
		return nil
	}

	maxLLM := score.MaxLLMPerCycle()
	if *llmCallsThisCycle >= maxLLM {
		// Circuit breaker tripped: persist unscored, do not call LLM.
		result := hunt.ScoreResult{FitBand: hunt.FitBandUnscored, ScoredAt: time.Now()}
		if err := store.SetJobScore(ctx, job.ID, result); err != nil {
			slog.WarnContext(ctx, "hunt worker: SetJobScore (circuit-breaker unscored) failed",
				slog.Int64("job_id", job.ID),
				slog.Any("error", err),
			)
		}
		return &result
	}

	// Run the full cascade scorer. Increment the counter only when the LLM was
	// actually called — stale/reject/nil-profile short-circuits spend zero budget.
	result := scoreJobIfCreated(ctx, outcome, job, profile, deps, store)
	if result.LLMCalled {
		*llmCallsThisCycle++
	}
	return &result
}

// scoreJobIfCreated scores a single OutcomeCreated job and persists the result.
// It is extracted as a separate function for unit testability (injected store + deps).
// No-op for any outcome other than OutcomeCreated — returns zero ScoreResult.
//
// Write failures from SetJobScore are logged but do not abort the cycle.
// The returned ScoreResult carries LLMCalled so the caller can update the
// per-cycle circuit-breaker counter only when an actual LLM call occurred.
func scoreJobIfCreated(
	ctx context.Context,
	outcome hunt.Outcome,
	job hunt.Job,
	profile *score.ScoringProfile,
	deps score.ScorerDeps,
	store jobScoreSetter,
) hunt.ScoreResult {
	if outcome != hunt.OutcomeCreated {
		return hunt.ScoreResult{}
	}

	result := score.Score(ctx, profile, job, deps)

	if err := store.SetJobScore(ctx, job.ID, result); err != nil {
		slog.WarnContext(ctx, "hunt worker: SetJobScore failed",
			slog.Int64("job_id", job.ID),
			slog.String("fit_band", result.FitBand),
			slog.Any("error", err),
		)
	}
	return result
}

// observeScore emits the Phase 6 fit-scoring metrics for a single scored job.
//
// Metric routing:
//   - FitBand=="stale"  → IncrHuntScoreFiltered("recency")
//   - FitBand=="reject" → IncrHuntScoreFiltered("jaccard")
//   - LLMResult != ""   → IncrHuntScoreLLM(sr.LLMResult)
//   - LLMCalled==true   → ObserveHuntFitScore(sr.FitScore)
//
// Called after scoreJobWithLimit returns (both ingest path and sweep path).
func observeScore(sr hunt.ScoreResult) {
	switch sr.FitBand {
	case "stale":
		engine.IncrHuntScoreFiltered("recency")
	case "reject":
		engine.IncrHuntScoreFiltered("jaccard")
	}
	if sr.LLMResult != "" {
		engine.IncrHuntScoreLLM(sr.LLMResult)
	}
	if sr.LLMCalled {
		engine.ObserveHuntFitScore(sr.FitScore)
	}
}

// runUnscoredSweep performs the end-of-cycle backfill: scores open jobs that
// have never been scored (scored_at IS NULL), or all open jobs when
// HUNT_SCORE_RESCORE_ALL=true is set (one-shot re-score).
//
// The sweep shares the per-cycle LLM budget (llmCallsThisCycle) with the
// ingest path so that a large backfill cannot exhaust the LLM ceiling on its
// own. Jobs processed by the sweep are NOT notified — the sweep is a backfill
// path for hunt_list/job_match consumption only.
//
// sweepLimit is read from HUNT_SCORE_SWEEP_LIMIT (default 50).
func runUnscoredSweep(
	ctx context.Context,
	store unscoredJobStore,
	profile *score.ScoringProfile,
	deps score.ScorerDeps,
	llmCallsThisCycle *int,
) {
	rescoreAll := env.Bool("HUNT_SCORE_RESCORE_ALL", false)
	sweepLimit := env.Int("HUNT_SCORE_SWEEP_LIMIT", 50)

	jobs, err := store.UnscoredOpenJobs(ctx, sweepLimit, rescoreAll)
	if err != nil {
		slog.WarnContext(ctx, "hunt worker: sweep UnscoredOpenJobs failed", slog.Any("error", err))
		return
	}
	if len(jobs) == 0 {
		return
	}

	scored := 0
	for _, j := range jobs {
		sr := scoreJobWithLimit(ctx, hunt.OutcomeCreated, j, profile, deps, store, llmCallsThisCycle)
		if sr != nil {
			observeScore(*sr)
			scored++
		}
	}
	slog.InfoContext(ctx, "hunt worker: sweep complete",
		slog.Int("swept", len(jobs)),
		slog.Int("scored", scored),
	)
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
