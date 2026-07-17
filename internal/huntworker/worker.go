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
	"sync/atomic"
	"time"

	"github.com/anatolykoptev/go-kit/breaker"
	"github.com/anatolykoptev/go-kit/env"
	"github.com/anatolykoptev/go-kit/retry"
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
//
// This value MUST exceed go-search's raw_web_search server-side ToolTimeout (90s,
// the go-mcpserver global default).  If platCtx fires before go-search finishes,
// the HTTP call fails with a transport error and the Degraded=true signal never
// arrives — discoverJobURLs falls back with source="local-fallback" instead of the
// distinct source="degraded-fallback", making the two failure classes
// indistinguishable in dashboards and alerts.
//
// Budget breakdown:
//   - discovery fan-out: parallel DISCOVERY_QUERY_VARIANTS goroutines each call
//     DiscoverBoardURLs(platCtx, …), which adds its own defaultDiscoveryTimeout
//     (100s) child deadline.  Parallel fan-out → dominated by slowest variant ≈ 100s.
//   - ATS board API fetch: typically 2–5 s per slug; 20 s headroom is sufficient.
//
// Current value: 90s server cap + 30s margin = 120s.
// The enclosing deadline must stay above the server cap; see also
// TestPerPlatformTimeout_ExceedsRawWebSearchServerCap and
// discovery.TestDefaultDiscoveryTimeout_ExceedsRawWebSearchServerCap.
const perPlatformTimeout = 120 * time.Second

// Worker runs a periodic ATS ingest cycle.
type Worker struct {
	store          *hunt.Store
	notifier       hunt.Notifier
	notifyMetric   func(outcome string) // wired to engine.IncrHuntNotify in production
	interval       time.Duration
	queries        []string
	scoringProfile *score.ScoringProfile // nil = scoring disabled
	scorerDeps     score.ScorerDeps
	// llmBreaker is the cross-cycle LLM circuit breaker (PF-2). When non-nil,
	// every scorerDeps.LLM call is routed through breaker.Execute so that a
	// sustained LLM failure storm trips the breaker and fast-fails subsequent
	// calls with breaker.ErrOpen (→ llm_error fail-open path in scoreJobIfCreated)
	// instead of issuing more failing calls. nil when HUNT_SCORE_BREAKER_ENABLED=false.
	llmBreaker *breaker.Breaker
	// llmFn is the underlying LLM call function (engine.CallLLM in production).
	// Held as a field so the breaker wrapper can route to it; tests override it
	// with a fake to exercise the real breaker wrapping path without a live LLM.
	llmFn func(ctx context.Context, prompt string) (string, error)
	// cycleRunning (BH-7) prevents overlapping ticks when a cycle takes
	// longer than HUNT_INGEST_INTERVAL. Without this guard, a slow cycle
	// (e.g., 75 min with 1h interval) causes the next tick to fire while
	// runCycle is still executing → concurrent DB upserts, 2x ATS API calls,
	// LLM budget confusion. CAS(false→true) on tick; store(false) on exit.
	cycleRunning atomic.Bool
}

// NewWorker builds a Worker from env vars.  Returns nil if the store is nil
// (hunt DB not configured — silently disabled, matching the store-nil pattern
// everywhere in go-job).
func NewWorker(store *hunt.Store) *Worker {
	if store == nil {
		return nil
	}
	queries := parseQueries(env.Str("HUNT_INGEST_QUERIES", defaultIngestQueries))
	w := &Worker{
		store:        store,
		interval:     env.MustDuration("HUNT_INGEST_INTERVAL", 6*time.Hour),
		queries:      queries,
		notifyMetric: engine.IncrHuntNotify,
		// scoringProfile is loaded lazily on first Run (requires DB + context).
		llmFn: engine.CallLLM,
		scorerDeps: score.ScorerDeps{
			Jaccard: func(profileKW, jobText string) float64 {
				kw := jobs.ExtractResumeKeywords(profileKW)
				return jobs.ScoreJobMatchCoverage(kw, jobText)
			},
		},
	}
	// PF-2: cross-cycle LLM circuit breaker. Wraps every scorerDeps.LLM call so
	// a sustained LLM failure storm trips the breaker (FailThreshold consecutive
	// errors) and fast-fails subsequent calls with breaker.ErrOpen instead of
	// issuing more failing calls. ErrOpen surfaces as an LLM error in
	// scoreJobIfCreated → llm_error fail-open path (degraded but not dead).
	// Disabled when HUNT_SCORE_BREAKER_ENABLED=false (LLM calls go direct).
	if env.MustBool("HUNT_SCORE_BREAKER_ENABLED", true) {
		w.llmBreaker = newLLMBreaker()
		w.scorerDeps.LLM = func(ctx context.Context, prompt string) (string, error) {
			return breaker.Execute(w.llmBreaker, func() (string, error) {
				return w.llmFn(ctx, prompt)
			})
		}
	} else {
		w.scorerDeps.LLM = w.llmFn
	}
	return w
}

// newLLMBreaker builds the cross-cycle LLM circuit breaker with the PF-2
// policy: trip after 3 consecutive LLM errors, stay open for 30m before a
// half-open probe. OnTrip sets the scoring_degraded gauge (silent-downgrade
// signal); OnRecover clears it. The hooks fire in a goroutine (breaker.go).
func newLLMBreaker() *breaker.Breaker {
	return breaker.New(breaker.Options{
		Name:          "llm-cross-cycle",
		FailThreshold: 3,
		OpenDuration:  30 * time.Minute,
		OnTrip: func(name string) {
			slog.Warn("hunt LLM cross-cycle breaker tripped", slog.String("breaker", name))
			engine.SetHuntScoringDegraded(true)
		},
		OnRecover: func(name string) {
			slog.Info("hunt LLM cross-cycle breaker recovered", slog.String("breaker", name))
			engine.SetHuntScoringDegraded(false)
		},
	})
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
// A non-numeric value panics (PF-8 fix: fail-fast on config errors).
// Out-of-range values clamp to [0,100] with a warning (PF-14 fix: a malformed
// knob must never silently open the gate or block all notifications).
func huntNotifyMinFit() int {
	n := env.MustInt("HUNT_NOTIFY_MIN_FIT", 0)
	if n < 0 {
		slog.Warn("hunt: HUNT_NOTIFY_MIN_FIT negative, clamping to 0 (gate open)", slog.Int("value", n))
		return 0
	}
	if n > 100 {
		slog.Warn("hunt: HUNT_NOTIFY_MIN_FIT >100, clamping to 100 (gate fully closed)", slog.Int("value", n))
		return 100
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
		if w.notifyMetric != nil {
			w.notifyMetric("notifier_disabled")
		}
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
			// BH-7: Skip tick if previous cycle is still running. A slow cycle
			// (e.g., ATS APIs hanging) can exceed HUNT_INGEST_INTERVAL; without
			// this guard, the next tick spawns a concurrent cycle → duplicate
			// upserts, 2x resource consumption, LLM budget confusion.
			if !w.cycleRunning.CompareAndSwap(false, true) {
				slog.Warn("hunt worker: previous cycle still running, skipping tick")
				continue
			}
			go func() {
				defer w.cycleRunning.Store(false)
				w.runCycle(ctx)
			}()
		}
	}
}

// runCycle executes one ingest cycle across all configured role queries and
// the three ATS platforms (greenhouse, lever, ashby).
func (w *Worker) runCycle(ctx context.Context) {
	start := time.Now()
	slog.Info("hunt worker: cycle start", slog.Int("queries", len(w.queries)))

	// ESC-2: reset scoring degradation flag at cycle start. Set to 1 during the
	// cycle when the circuit breaker trips or the fail-open path is taken.
	engine.SetHuntScoringDegraded(false)

	platforms := []struct {
		name   string
		search func(ctx context.Context, query, loc string, limit int) ([]engine.SearxngResult, error)
	}{
		{engine.DiscoveryPlatformGreenhouse, jobs.SearchGreenhouseJobs},
		{engine.DiscoveryPlatformLever, jobs.SearchLeverJobs},
		{engine.DiscoveryPlatformAshby, jobs.SearchAshbyJobs},
	}

	var totalCreated, totalMerged, totalError int
	var llmCallsThisCycle atomic.Int64 // circuit-breaker counter, reset per cycle (BH-4)

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
		slog.Int("llm_scored", int(llmCallsThisCycle.Load())),
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
// llmCallsThisCycle is incremented when the LLM was ATTEMPTED
// (ScoreResult.LLMResult != ""). This includes parse_fail and llm_error paths
// so that a proxy-down storm cannot issue unlimited calls (MEDIUM-2).
// Stale, sub-Jaccard, and nil-profile jobs short-circuit before the LLM
// (LLMResult=="") and must NOT consume budget.
func scoreJobWithLimit(
	ctx context.Context,
	outcome hunt.Outcome,
	job hunt.Job,
	profile *score.ScoringProfile,
	deps score.ScorerDeps,
	store jobScoreSetter,
	llmCallsThisCycle *atomic.Int64,
) *hunt.ScoreResult {
	if outcome != hunt.OutcomeCreated {
		return nil
	}

	maxLLM := score.MaxLLMPerCycle()
	if llmCallsThisCycle.Load() >= int64(maxLLM) {
		// Circuit breaker tripped: return unscored result in-memory only.
		// Do NOT call SetJobScore — persisting scored_at=NOW() would remove
		// the job from the `scored_at IS NULL` unscored pool, permanently
		// stranding it without LLM scoring. The sweep (runUnscoredSweep)
		// will pick it up in the next cycle when budget is available.
		engine.IncrHuntScoreBreakerTrips()
		// ESC-2: breaker open = scoring degraded.
		engine.SetHuntScoringDegraded(true)
		result := hunt.ScoreResult{FitBand: hunt.FitBandUnscored}
		return &result
	}

	// Run the full cascade scorer. Increment the counter when the LLM was
	// ATTEMPTED (LLMResult != "") — this includes parse_fail and llm_error so
	// a proxy-down storm cannot issue unlimited calls (MEDIUM-2 fix).
	// Stale/reject/nil-profile short-circuits have LLMResult=="" and spend zero budget.
	result := scoreJobIfCreated(ctx, outcome, job, profile, deps, store)
	if result.LLMResult != "" {
		llmCallsThisCycle.Add(1)
	}
	return &result
}

// scoreJobIfCreated scores a single OutcomeCreated job and persists the result.
// It is extracted as a separate function for unit testability (injected store + deps).
// No-op for any outcome other than OutcomeCreated — returns zero ScoreResult.
//
// Write failures from SetJobScore are retried up to 5 times with exponential
// backoff (go-kit/retry.Do). Transient DB errors (connection blips, timeouts)
// are retried; permanent errors (ErrNotFound — job deleted between UpsertJob
// and SetJobScore) are not retried. After all retries are exhausted, the
// failure is logged and a metric is incremented — the job stays in the
// unscored pool (scored_at IS NULL) for the sweep to retry in the next cycle.
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

	// ESC-2: signal scoring degradation when the fail-open path is taken
	// (LLM error or JSON parse failure → job lands as unscored).
	if result.LLMResult == "llm_error" || result.LLMResult == "parse_fail" {
		engine.SetHuntScoringDegraded(true)
	}

	_, err := retry.Do(ctx, retry.Options{
		MaxAttempts:  5,
		InitialDelay: 500 * time.Millisecond,
		MaxDelay:     5 * time.Second,
		Jitter:       true,
		AbortOn:      []error{hunt.ErrNotFound},
		OnRetry: func(attempt int, err error) {
			slog.WarnContext(ctx, "hunt worker: SetJobScore retry",
				slog.Int64("job_id", job.ID),
				slog.Int("attempt", attempt),
				slog.Any("error", err),
			)
		},
	}, func() (struct{}, error) {
		return struct{}{}, store.SetJobScore(ctx, job.ID, result)
	})
	if err != nil {
		slog.WarnContext(ctx, "hunt worker: SetJobScore failed after retries",
			slog.Int64("job_id", job.ID),
			slog.String("fit_band", result.FitBand),
			slog.Any("error", err),
		)
		engine.IncrHuntScorePersistFailures()
	}
	return result
}

// observeScore emits the Phase 6 fit-scoring metrics for a single scored job.
//
// Metric routing:
//   - FitBand==FitBandStale   → IncrHuntScoreFiltered("recency")
//   - FitBand==FitBandReject  → IncrHuntScoreFiltered("jaccard")
//   - FitBand==FitBandQuality → IncrHuntScoreFiltered("quality")
//   - LLMResult != ""         → IncrHuntScoreLLM(sr.LLMResult)
//   - LLMResult=="ok"|"enum_clamp" → ObserveHuntFitScore(sr.FitScore)
//
// The histogram fires only on "ok" and "enum_clamp" (LLM returned a real
// fit_score). "parse_fail"/"llm_error" results carry a Jaccard fallback
// FitScore that must NOT pollute hunt_fit_score.
//
// Called after scoreJobWithLimit returns (both ingest path and sweep path).
func observeScore(sr hunt.ScoreResult) {
	switch sr.FitBand {
	case hunt.FitBandStale:
		engine.IncrHuntScoreFiltered("recency")
	case hunt.FitBandReject:
		engine.IncrHuntScoreFiltered("jaccard")
	case hunt.FitBandQuality:
		engine.IncrHuntScoreFiltered("quality")
	}
	if sr.LLMResult != "" {
		engine.IncrHuntScoreLLM(sr.LLMResult)
	}
	if sr.LLMResult == "ok" || sr.LLMResult == "enum_clamp" {
		engine.ObserveHuntFitScore(sr.FitScore)
	}
}

// runUnscoredSweep performs the end-of-cycle backfill: scores open jobs that
// have never been scored (scored_at IS NULL), or all open jobs when
// HUNT_SCORE_RESCORE_ALL=true is set (one-shot re-score).
//
// The sweep shares the per-cycle LLM budget (llmCallsThisCycle) with the
// ingest path. The fetch is capped at the REMAINING budget to guarantee that
// budget-starved jobs are never persisted with scored_at=now() before being
// LLM-scored, which would remove them from the unscored pool permanently
// (MEDIUM-1). If the budget is already exhausted, the sweep returns early
// without calling UnscoredOpenJobs.
//
// Jobs processed by the sweep are NOT notified — the sweep is a backfill
// path for hunt_list/job_match consumption only.
//
// sweepLimit is read from HUNT_SCORE_SWEEP_LIMIT (default 50).
func runUnscoredSweep(
	ctx context.Context,
	store unscoredJobStore,
	profile *score.ScoringProfile,
	deps score.ScorerDeps,
	llmCallsThisCycle *atomic.Int64,
) {
	rescoreAll := env.MustBool("HUNT_SCORE_RESCORE_ALL", false)
	sweepLimit := env.MustInt("HUNT_SCORE_SWEEP_LIMIT", 50)

	// MEDIUM-1 budget cap: only fetch as many jobs as there is remaining LLM
	// budget. Jobs that would exceed the ceiling could end up with
	// scored_at=now() + FitBand=unscored (via the circuit-breaker inside
	// scoreJobWithLimit), removing them from the `scored_at IS NULL` pool
	// permanently without ever being LLM-scored.
	//
	// Note: stale/reject jobs that short-circuit BEFORE the LLM still get
	// scored legitimately (they don't consume budget); only LLM-needing jobs
	// are at risk. The cap is conservative — it limits the fetch, not just
	// the LLM call count. A larger sweep limit just under-utilizes, never
	// permanently strands jobs.
	maxLLM := score.MaxLLMPerCycle()
	remaining := maxLLM - int(llmCallsThisCycle.Load())
	if remaining <= 0 {
		return
	}
	fetch := sweepLimit
	if remaining < fetch {
		fetch = remaining
	}

	jobs, err := store.UnscoredOpenJobs(ctx, fetch, rescoreAll)
	if err != nil {
		slog.WarnContext(ctx, "hunt worker: sweep UnscoredOpenJobs failed", slog.Any("error", err))
		return
	}

	// ESC-2: set unscored-jobs gauges from the sweep result (no extra SQL query).
	// Aggregate count and oldest first_seen_at in Go from the UnscoredOpenJobs
	// result (which returns first_seen_at and is ordered ASC by first_seen_at).
	count := float64(len(jobs))
	var maxAge float64
	if count > 0 {
		var oldest time.Time
		for _, j := range jobs {
			if oldest.IsZero() || j.FirstSeenAt.Before(oldest) {
				oldest = j.FirstSeenAt
			}
		}
		maxAge = time.Since(oldest).Seconds()
	}
	engine.SetHuntUnscoredJobsCount(count)
	engine.SetHuntUnscoredJobsMaxAge(maxAge)

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
