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
//	huntworker → accounts (panel_accounts enumeration + account_hunt_settings)
//	huntworker → engine (CallLLM)
//	huntworker → engine/jobs (ScoreJobMatchCoverage, search functions)
//	huntworker → hunt (Store, Job, ScoreResult, Outcome)
//	huntworker → hunt/score (Score, ScorerDeps, ScoringProfile)
//	hunt/score → hunt (types only, no engine — cycle-free)
//
// P3 account model (plan ADR-7): the worker derives its account scope — it is
// NOT handed one. Every cycle enumerates ACTIVE panel_accounts LEFT JOIN
// account_hunt_settings (accounts.ListAccountHuntSettings): a missing row or
// enabled=false skips the account (provisioning never silently arms a hunt).
// Ingest consumes the UNION of enabled accounts' query lists against the
// SHARED hunt_jobs corpus; judgments (account_job_scores, hunt_ratings) are
// written per-account through the ForAccount facade; notification routes
// per-account via notify_chat_id/notify_min_fit/notify_max_age_seconds.
// uuid.Nil never names a scoring account — its only meaning is the
// corpus-only ingest fallback when zero accounts are enabled.
//
// Fleet-global knobs stay env/deploy config: HUNT_INGEST_INTERVAL (ticker),
// HUNT_SCORE_MAX_LLM_PER_CYCLE (fleet LLM cap — per-account
// score_max_llm_per_cycle can only tighten it), HUNT_SCORE_SWEEP_LIMIT.
// Gate: HUNT_INGEST_ENABLED=false explicitly disables the worker; unset/true
// lets the per-account enumeration decide (accounts with no row are skipped).
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
	"github.com/anatolykoptev/go_job/internal/accounts"
	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/anatolykoptev/go_job/internal/hunt/score"
	"github.com/google/uuid"
)

// jobScoreSetter is the narrow store interface used by scoring helpers.
// *hunt.AccountStore satisfies this; tests inject a fake.
type jobScoreSetter interface {
	SetJobScore(ctx context.Context, id int64, sr hunt.ScoreResult) error
}

// unscoredJobStore is the narrow store interface used by the end-of-cycle
// unscored-open sweep. Implemented by *hunt.AccountStore; tests inject a fake.
type unscoredJobStore interface {
	jobScoreSetter
	UnscoredOpenJobs(ctx context.Context, limit int, rescoreAll bool) ([]hunt.Job, error)
}

// unscoredJobStatsStore is the narrow store interface used by the periodic
// gauge refresher (refreshUnscoredGauges). It returns count + oldest age
// without fetching full job rows — a single SELECT COUNT(*), MIN(first_seen_at).
// Implemented by *hunt.AccountStore; tests inject a fake.
type unscoredJobStatsStore interface {
	UnscoredOpenJobsStats(ctx context.Context) (hunt.UnscoredJobsStats, error)
}

// accountScoreStore is the account-scoped scoring surface the worker drives
// (plan ADR-15): *hunt.AccountStore — bound via store.ForAccount(aid) — in
// production; tests inject fakes. It bundles the three scoped ops the worker
// needs: persist a score row for the account, fetch the account's unscored
// pool for the end-of-cycle sweep, and the pool's COUNT/MIN stats for gauges.
// The unscoped hunt_jobs.fit_* surface is deliberately gone — a score without
// an owning account is not expressible.
type accountScoreStore interface {
	jobScoreSetter
	unscoredJobStatsStore
	UnscoredOpenJobs(ctx context.Context, limit int, rescoreAll bool) ([]hunt.Job, error)
}

// fleetLLMCap is the fleet-wide per-cycle LLM budget ceiling
// (HUNT_SCORE_MAX_LLM_PER_CYCLE, default 50). Per-account
// score_max_llm_per_cycle values can only TIGHTEN it — a NULL account setting
// means the account shares the fleet cap.
func fleetLLMCap() int {
	return env.MustInt("HUNT_SCORE_MAX_LLM_PER_CYCLE", 50)
}

// envEqualFold reads an env var and compares case-insensitively to val.
func envEqualFold(key, val string) bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(key)), val)
}

// clampNotifyMinFit clamps the fit-gate threshold to [0,100] with a warning
// on out-of-range values (PF-14 fix: a malformed knob must never silently open
// the gate or block all notifications).
func clampNotifyMinFit(n int) int {
	if n < 0 {
		slog.Warn("hunt: notify_min_fit negative, clamping to 0 (gate open)", slog.Int("value", n))
		return 0
	}
	if n > 100 {
		slog.Warn("hunt: notify_min_fit >100, clamping to 100 (gate fully closed)", slog.Int("value", n))
		return 100
	}
	return n
}

// defaultIngestQueries are generic role/skill strings used when no account's
// queries apply. No company names, no personal targets — PUBLIC-repo-safe.
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

// ── per-account cycle plan ──────────────────────────────────────────────────

// llmBudget bundles the two per-cycle LLM counters a score may spend:
//
//   - acct  — the account's own counter, capped by the account's EFFECTIVE cap
//     (min(score_max_llm_per_cycle, fleet cap); deps.Settings.MaxLLMPerCycle
//     already carries the effective value)
//   - fleet — the fleet-wide counter shared across all accounts this cycle,
//     capped by fleetCap (HUNT_SCORE_MAX_LLM_PER_CYCLE)
//
// A nil fleet pointer means "no fleet gate" — single-account test fakes and
// the corpus-only ingest path stay simple. consume() is called ONLY when an
// LLM call was actually attempted (LLMResult != "") — stale/reject/nil-profile
// short-circuits spend zero budget.
type llmBudget struct {
	acct     *atomic.Int64
	fleet    *atomic.Int64
	fleetCap int
}

// exhausted reports whether EITHER counter is at its cap.
func (b *llmBudget) exhausted(perAcctCap int) bool {
	if b.acct.Load() >= int64(perAcctCap) {
		return true
	}
	return b.fleet != nil && b.fleet.Load() >= int64(b.fleetCap)
}

// consume charges one attempted LLM call to both counters.
func (b *llmBudget) consume() {
	b.acct.Add(1)
	if b.fleet != nil {
		b.fleet.Add(1)
	}
}

// acctRemaining reports how many LLM calls this account may still spend this
// cycle under BOTH its own cap and the fleet cap.
func (b *llmBudget) acctRemaining(perAcctCap int) int {
	r := perAcctCap - int(b.acct.Load())
	if b.fleet != nil {
		if f := b.fleetCap - int(b.fleet.Load()); f < r {
			r = f
		}
	}
	if r < 0 {
		return 0
	}
	return r
}

// accountPlan is one enabled account's resolved per-cycle configuration: the
// bound scoring facade, the env-merged settings, the per-account scorer deps
// (ScoringSettings derived from the account row), the account-bound notifier,
// and the cycle's LLM budget counters. Built fresh every cycle by
// loadAccountPlans — account settings apply without a redeploy.
type accountPlan struct {
	aid      uuid.UUID
	scores   accountScoreStore
	settings accounts.AccountHuntSettings
	deps     score.ScorerDeps
	notifier hunt.Notifier // account-bound clone (chat id + max age); nil → metric only
	budget   *llmBudget
}

// scoreEnabled reports whether this account may score this cycle — its own
// score_enabled flag AND a loaded scoring profile (nil profile → scorer
// short-circuits anyway; the flag keeps the sweep from even fetching).
func (p *accountPlan) scoreEnabled(profile *score.ScoringProfile) bool {
	return profile != nil && p.settings.ScoreEnabled && p.scores != nil
}

// maybeNotifyJob applies THIS ACCOUNT's fit gate and fires its account-bound
// notifier when the outcome is OutcomeCreated and the job is open (or has
// empty status — see SearxngResultToHuntJob's empty-status note in worker.go).
//
// Gate table (unchanged semantics, now per-account):
//   - score == nil (scoring disabled)        → notify (recency-only card)
//   - score.FitBand == "unscored" (LLM fail) → notify (degraded card)
//   - score.FitScore < account's min_fit     → skip notify, metric "low_fit"
//   - else                                    → notify (full fit-card)
//
// Metric ownership (no double-count): the ONLY outcome this method emits is
// "low_fit" — and only on the terminal-drop path that returns without
// dispatch. All other outcomes (sent/failed/stale/no_date/unscored) are
// emitted by the notifier AFTER ITS OWN recency gate, so a stale unscored job
// counts once ("stale"), not twice. See ProductNotifier.NotifyNewJob.
//
// Empty status is treated as open because SearxngResultToHuntJob does not set
// a Status field — UpsertJob normalises it to StatusOpen in Postgres, but the
// in-memory Job struct retains "".
func (p *accountPlan) maybeNotifyJob(j hunt.Job, outcome hunt.Outcome, sr *hunt.ScoreResult, notifyMetric func(string)) {
	if outcome != hunt.OutcomeCreated {
		return
	}
	if p.notifier == nil {
		if notifyMetric != nil {
			notifyMetric("notifier_disabled")
		}
		return
	}
	if j.Status != hunt.StatusOpen && j.Status != "" {
		return
	}

	// Fit gate — only a REAL score (not nil, not "unscored") is gated, by THIS
	// account's notify_min_fit. A sub-threshold real score is a TERMINAL drop:
	// the job is not dispatched and "low_fit" is emitted here exactly once.
	if sr != nil && sr.FitBand != hunt.FitBandUnscored {
		minFit := p.settings.NotifyMinFit
		if minFit > 0 && sr.FitScore < minFit {
			if notifyMetric != nil {
				notifyMetric("low_fit")
			}
			slog.Debug("hunt worker: fit gate dropped job",
				slog.Int64("job_id", j.ID),
				slog.Int("fit_score", sr.FitScore),
				slog.Int("min_fit", minFit),
				slog.String("account_id", p.aid.String()),
			)
			return
		}
	}

	// nil score (scoring disabled), unscored (LLM-fail fail-open), or fit ≥
	// threshold: dispatch to the account-bound notifier, which owns the
	// recency gate and emits the terminal outcome.
	p.notifier.NotifyNewJob(j, sr)
}

// Worker runs a periodic ATS ingest cycle.
type Worker struct {
	store    *hunt.Store
	notifier hunt.Notifier
	// accountNotify builds a per-account notifier bound to (chatID, maxAge).
	// Production: clones the *notify.ProductNotifier sink via ForChat; tests
	// inject a fake to capture the per-account routing. nil → the base
	// notifier is reused for every account.
	accountNotify func(chatID int64, maxAge time.Duration) hunt.Notifier
	// listAccounts enumerates ACTIVE panel_accounts LEFT JOIN
	// account_hunt_settings (accounts.ListAccountHuntSettings in production;
	// tests inject fakes). Called at the top of every cycle — account changes
	// apply on the next tick.
	listAccounts func(ctx context.Context) ([]accounts.AccountHuntSettings, error)
	// forAccount binds the account-scoped score facade (store.ForAccount in
	// production; tests inject fakes).
	forAccount     func(aid uuid.UUID) accountScoreStore
	notifyMetric   func(outcome string)  // wired to engine.IncrHuntNotify in production
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
	w := &Worker{
		store:        store,
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
			engine.SetHuntScoringDegraded(true, "breaker_open")
		},
		OnRecover: func(name string) {
			slog.Info("hunt LLM cross-cycle breaker recovered", slog.String("breaker", name))
			engine.SetHuntScoringDegraded(false, "breaker_recovered")
		},
	})
}

// SetNotifier wires the base Telegram notifier into the worker.
// Must be called before Run(). Optional — if nil, no notifications are sent.
// Per-cycle, each enabled account gets a notifier bound to ITS OWN
// notify_chat_id + notify_max_age via accountNotify (ProductNotifier.ForChat);
// when the base is nil or cannot clone, every account falls back to the base
// notifier (or none).
func (w *Worker) SetNotifier(n hunt.Notifier) {
	w.notifier = n
	w.accountNotify = func(chatID int64, maxAge time.Duration) hunt.Notifier {
		if pn, ok := n.(interface {
			ForChat(int64, time.Duration) hunt.Notifier
		}); ok && chatID != 0 {
			return pn.ForChat(chatID, maxAge)
		}
		return n
	}
}

// mergeAccountSettings resolves one account's runtime settings: the DB row is
// authoritative for every bool (enabled/score_enabled/score_fail_open can
// never be re-armed by env), env vars fill only zero-valued scalar fields —
// the same per-field merge contract the legacy single-row LoadSettings had.
func mergeAccountSettings(r accounts.AccountHuntSettings) accounts.AccountHuntSettings {
	s := accounts.AccountHuntSettings{
		AccountID:           r.AccountID,
		HasRow:              r.HasRow,
		Enabled:             r.Enabled, // DB-authoritative
		Queries:             env.Str("HUNT_INGEST_QUERIES", defaultIngestQueries),
		NotifyChatID:        int64(env.MustInt("HUNT_NOTIFY_CHAT_ID", 0)),
		NotifyMinFit:        clampNotifyMinFit(env.MustInt("HUNT_NOTIFY_MIN_FIT", 0)),
		NotifyMaxAge:        env.MustDuration("HUNT_NOTIFY_MAX_AGE", 48*time.Hour),
		ScoreEnabled:        r.ScoreEnabled, // DB-authoritative
		ScoreMinJaccard:     env.MustInt("HUNT_SCORE_MIN_JACCARD", 8),
		ScoreMaxLLMPerCycle: r.ScoreMaxLLMPerCycle,
		ScoreFailOpen:       r.ScoreFailOpen, // DB-authoritative
		UpdatedAt:           r.UpdatedAt,
	}
	if r.Queries != "" {
		s.Queries = r.Queries
	}
	if r.NotifyChatID > 0 {
		s.NotifyChatID = r.NotifyChatID
	}
	if r.NotifyMinFit > 0 {
		s.NotifyMinFit = clampNotifyMinFit(r.NotifyMinFit)
	}
	if r.NotifyMaxAge > 0 {
		s.NotifyMaxAge = r.NotifyMaxAge
	}
	if r.ScoreMinJaccard > 0 {
		s.ScoreMinJaccard = r.ScoreMinJaccard
	}
	return s
}

// loadAccountPlans enumerates ACTIVE panel_accounts LEFT JOIN
// account_hunt_settings and builds one accountPlan per ENABLED account —
// the worker's account census for this cycle (plan ADR-7). A missing
// settings row (HasRow=false) or enabled=false skips the account silently;
// deactivated accounts are absent upstream. fleetUsed is the shared per-cycle
// fleet LLM counter each plan's budget references.
func (w *Worker) loadAccountPlans(ctx context.Context, fleetUsed *atomic.Int64) []accountPlan {
	rows, err := w.listAccounts(ctx)
	if err != nil {
		slog.WarnContext(ctx, "hunt worker: account enumeration failed — cycle uses no accounts",
			slog.Any("error", err))
		return nil
	}
	fleetCap := fleetLLMCap()
	plans := make([]accountPlan, 0, len(rows))
	for _, r := range rows {
		if !r.HasRow || !r.Enabled {
			continue // missing row or disabled → skip (ADR-7)
		}
		s := mergeAccountSettings(r)

		// Effective per-account LLM cap: the account's optional sub-cap can
		// only TIGHTEN the fleet cap, never loosen it.
		effCap := fleetCap
		if s.ScoreMaxLLMPerCycle != nil && *s.ScoreMaxLLMPerCycle > 0 && *s.ScoreMaxLLMPerCycle < fleetCap {
			effCap = *s.ScoreMaxLLMPerCycle
		}

		deps := w.scorerDeps
		failOpen := s.ScoreFailOpen
		deps.Settings = &score.ScoringSettings{
			NotifyMaxAge:   s.NotifyMaxAge,
			MinJaccard:     float64(s.ScoreMinJaccard),
			FailOpen:       &failOpen,
			MaxLLMPerCycle: effCap,
		}

		var n hunt.Notifier
		if w.accountNotify != nil {
			n = w.accountNotify(s.NotifyChatID, s.NotifyMaxAge)
		} else {
			n = w.notifier
		}

		var acctStore accountScoreStore
		if w.forAccount != nil {
			acctStore = w.forAccount(r.AccountID)
		}

		plans = append(plans, accountPlan{
			aid:      r.AccountID,
			scores:   acctStore,
			settings: s,
			deps:     deps,
			notifier: n,
			budget:   &llmBudget{acct: &atomic.Int64{}, fleet: fleetUsed, fleetCap: fleetCap},
		})
	}
	return plans
}

// unionQueries collects the enabled plans' query lists, deduplicated while
// preserving first-seen order (a query shared by several accounts is fetched
// once — the corpus row is global, only the judgments are per-account).
func unionQueries(plans []accountPlan) []string {
	seen := make(map[string]bool)
	var out []string
	for _, p := range plans {
		for _, q := range parseQueries(p.settings.Queries) {
			if !seen[q] {
				seen[q] = true
				out = append(out, q)
			}
		}
	}
	return out
}

// parseQueries splits a comma-separated query string, trims whitespace, and
// drops empty entries. An empty input falls back to defaultIngestQueries —
// matching the legacy HUNT_INGEST_QUERIES default contract.
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
	interval := env.MustDuration("HUNT_INGEST_INTERVAL", 6*time.Hour)
	slog.Info("hunt worker: starting", slog.Duration("interval", interval))

	// Load the scoring profile once at startup (requires context + DB).
	// The profile itself is fleet-global (one resume profile); per-account
	// scoring decisions happen per-cycle in loadAccountPlans.
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

	// Periodic gauge refresher: update gojob_hunt_unscored_jobs_count{account}
	// and gojob_hunt_unscored_jobs_max_age_seconds{account} between hunt
	// cycles so the alert max(gojob_hunt_unscored_jobs_max_age_seconds) > 7200
	// reflects the LIVE per-account backlog, not a value frozen at the end of
	// the last 6h cycle. The refresher runs on its own ticker (default 10m,
	// configurable via HUNT_SCORE_GAUGE_REFRESH_INTERVAL) and does a single
	// COUNT+MIN query per enabled account — no row fetch, no LLM call, no
	// scoring. It is safe to run concurrently with an in-progress cycle.
	gaugeRefreshInterval := env.MustDuration("HUNT_SCORE_GAUGE_REFRESH_INTERVAL", 10*time.Minute)
	gaugeTicker := time.NewTicker(gaugeRefreshInterval)
	defer gaugeTicker.Stop()
	slog.Info("hunt worker: gauge refresher started", slog.Duration("interval", gaugeRefreshInterval))

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("hunt worker: stopping")
			return
		case <-gaugeTicker.C:
			w.refreshAllUnscoredGauges(ctx)
		case <-ticker.C:
			// BH-7: Skip tick if previous cycle is still running. A slow cycle
			// (e.g., ATS APIs hanging) can exceed HUNT_INGEST_INTERVAL; without
			// this guard, the next tick spawns a concurrent cycle → duplicate
			// upserts, 2x resource consumption, LLM budget confusion.
			if !w.cycleRunning.CompareAndSwap(false, true) {
				slog.Warn("hunt worker: previous cycle still running — skipping tick")
				continue
			}
			go func() {
				defer w.cycleRunning.Store(false)
				w.runCycle(ctx)
			}()
		}
	}
}

// refreshAllUnscoredGauges re-enumerates enabled accounts and refreshes each
// account's unscored-pool gauges independently (label account=<uuid>).
func (w *Worker) refreshAllUnscoredGauges(ctx context.Context) {
	var fleet atomic.Int64 // not needed for gauge refresh; satisfies budget shape
	for _, p := range w.loadAccountPlans(ctx, &fleet) {
		if p.scores == nil {
			continue
		}
		refreshUnscoredGauges(ctx, p.scores, p.aid.String())
	}
}

// runCycle executes one ingest cycle: enumerate enabled accounts, ingest the
// union of their query lists into the SHARED corpus, then score + notify each
// created job under EVERY enabled account (round-robin per job), and finally
// run each account's unscored sweep under its own sub-cap + the fleet cap.
func (w *Worker) runCycle(ctx context.Context) {
	start := time.Now()

	// Account census for this cycle — settings changes apply without redeploy.
	var fleetUsed atomic.Int64
	plans := w.loadAccountPlans(ctx, &fleetUsed)

	queries := unionQueries(plans)
	if len(queries) == 0 {
		// Zero enabled accounts. uuid.Nil's only remaining meaning: the
		// corpus-only ingest fallback — HUNT_INGEST_ENABLED=true keeps the
		// shared corpus warm from env queries even with no account armed.
		// Scores and notify stay off (there is no account to attach them to).
		if !envEqualFold("HUNT_INGEST_ENABLED", "true") {
			slog.Debug("hunt worker: no enabled accounts — cycle skipped")
			return
		}
		queries = parseQueries(env.Str("HUNT_INGEST_QUERIES", defaultIngestQueries))
		slog.Info("hunt worker: no enabled accounts — corpus-only ingest",
			slog.Int("queries", len(queries)))
	} else {
		slog.Info("hunt worker: cycle start",
			slog.Int("accounts", len(plans)),
			slog.Int("queries", len(queries)),
		)
	}

	// ESC-2: reset scoring degradation flag at cycle start. Set to 1 during the
	// cycle when the circuit breaker trips or the fail-open path is taken.
	engine.SetHuntScoringDegraded(false, "cycle_reset")

	platforms := []struct {
		name   string
		search func(ctx context.Context, query, loc string, limit int) ([]engine.SearxngResult, error)
	}{
		{engine.DiscoveryPlatformGreenhouse, jobs.SearchGreenhouseJobs},
		{engine.DiscoveryPlatformLever, jobs.SearchLeverJobs},
		{engine.DiscoveryPlatformAshby, jobs.SearchAshbyJobs},
	}

	var totalCreated, totalMerged, totalError int

	for _, q := range queries {
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
						// Round-robin per account: each enabled account scores
						// the new corpus row under its own sub-cap + the shared
						// fleet counter, then applies its own notify gates.
						for i := range plans {
							plan := &plans[i]
							var sr *hunt.ScoreResult
							if plan.scoreEnabled(w.scoringProfile) {
								sr = scoreJobWithLimit(ctx, outcome, j,
									w.scoringProfile, plan.deps, plan.scores, plan.budget)
								if sr != nil {
									observeScore(*sr)
								}
							}
							plan.maybeNotifyJob(j, outcome, sr, w.notifyMetric)
						}
					case outcome == hunt.OutcomeMerged:
						totalMerged++
					}
				}
			}()
		}
	}

	// End-of-cycle unscored-open sweep PER ACCOUNT: score open jobs that have
	// no account_job_scores row for that account, bounded by the account's
	// effective sub-cap and the fleet counter they all share.
	sweepLimit := env.MustInt("HUNT_SCORE_SWEEP_LIMIT", 50)
	for i := range plans {
		plan := &plans[i]
		if plan.scoreEnabled(w.scoringProfile) {
			runUnscoredSweep(ctx, plan.scores, w.scoringProfile, plan.deps, plan.budget, sweepLimit, plan.aid.String())
		}
	}

	elapsed := time.Since(start)
	engine.ObserveHuntCycleDuration(elapsed.Seconds())
	slog.Info("hunt worker: cycle complete",
		slog.Duration("elapsed", elapsed),
		slog.Int("accounts", len(plans)),
		slog.Int("created", totalCreated),
		slog.Int("merged", totalMerged),
		slog.Int("errors", totalError),
		slog.Int64("llm_scored_fleet", fleetUsed.Load()),
	)
}

// scoreJobWithLimit scores a job, enforcing the per-cycle LLM budget
// (per-account cap AND shared fleet cap via budget). If either counter is
// exhausted, the job is returned as "unscored" without calling the LLM.
//
// Returns a pointer to the ScoreResult so the caller (runCycle) can thread it
// into maybeNotifyJob for the fit gate and card rendering. Returns nil when
// outcome is not OutcomeCreated (no scoring performed).
//
// budget.consume() runs when the LLM was ATTEMPTED (ScoreResult.LLMResult !=
// ""). This includes parse_fail and llm_error paths so that a proxy-down storm
// cannot issue unlimited calls (MEDIUM-2). Stale, sub-Jaccard, and nil-profile
// jobs short-circuit before the LLM (LLMResult=="") and must NOT consume budget.
func scoreJobWithLimit(
	ctx context.Context,
	outcome hunt.Outcome,
	job hunt.Job,
	profile *score.ScoringProfile,
	deps score.ScorerDeps,
	store jobScoreSetter,
	budget *llmBudget,
) *hunt.ScoreResult {
	if outcome != hunt.OutcomeCreated {
		return nil
	}

	maxLLM := score.MaxLLMPerCycle(deps.Settings)
	if budget.exhausted(maxLLM) {
		// Per-cycle LLM budget exhausted: return unscored result in-memory only.
		// Do NOT call SetJobScore — persisting scored_at=NOW() would remove
		// the job from the account's unscored pool, permanently stranding it
		// without LLM scoring. The sweep (runUnscoredSweep) will pick it up in
		// the next cycle when budget is available.
		//
		// Budget exhaustion is NORMAL operation, not degradation — the gauge
		// must NOT be set. The skipped_budget LLMResult makes these jobs
		// countable via gojob_hunt_score_llm_total{result="skipped_budget"}
		// without falsely triggering the GojobHuntScoringDegraded alert.
		engine.IncrHuntScoreBreakerTrips()
		result := hunt.ScoreResult{FitBand: hunt.FitBandUnscored, LLMResult: "skipped_budget"}
		return &result
	}

	// Run the full cascade scorer. Charge the budget when the LLM was
	// ATTEMPTED (LLMResult != "") — this includes parse_fail and llm_error so
	// a proxy-down storm cannot issue unlimited calls (MEDIUM-2 fix).
	// Stale/reject/nil-profile short-circuits have LLMResult=="" and spend zero budget.
	result := scoreJobIfCreated(ctx, outcome, job, profile, deps, store)
	if result.LLMResult != "" {
		budget.consume()
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
// account's unscored pool for the sweep to retry in the next cycle.
// The returned ScoreResult carries LLMCalled so the caller can update the
// per-cycle budget counters only when an actual LLM call occurred.
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
		engine.SetHuntScoringDegraded(true, result.LLMResult)
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

// runUnscoredSweep performs the end-of-cycle backfill FOR ONE ACCOUNT: scores
// open jobs that have no account_job_scores row for that account
// (rescore-all under HUNT_SCORE_RESCORE_ALL=true is the one-shot re-score).
//
// The sweep shares the per-cycle LLM budget (account counter AND fleet
// counter via budget) with the ingest path. The fetch is capped at the
// REMAINING budget to guarantee that budget-starved jobs are never persisted
// with scored_at=now() before being LLM-scored, which would remove them from
// the account's unscored pool permanently (MEDIUM-1). If the budget is
// already exhausted, the sweep returns early without calling UnscoredOpenJobs.
//
// Jobs processed by the sweep are NOT notified — the sweep is a backfill
// path for hunt_list/job_match consumption only.
//
// sweepLimit is the fleet-global max unscored-open jobs to backfill per cycle
// (env HUNT_SCORE_SWEEP_LIMIT, default 50). account labels the account's
// unscored gauges (gojob_hunt_unscored_jobs_*{account}).
func runUnscoredSweep(
	ctx context.Context,
	store unscoredJobStore,
	profile *score.ScoringProfile,
	deps score.ScorerDeps,
	budget *llmBudget,
	sweepLimit int,
	account string,
) {
	rescoreAll := env.MustBool("HUNT_SCORE_RESCORE_ALL", false)
	if sweepLimit <= 0 {
		sweepLimit = 50
	}

	// MEDIUM-1 budget cap: only fetch as many jobs as there is remaining LLM
	// budget under BOTH the account sub-cap and the fleet cap. Jobs that would
	// exceed the ceiling could end up with scored_at=now() + FitBand=unscored
	// (via the circuit-breaker inside scoreJobWithLimit), removing them from
	// the account's unscored pool permanently without ever being LLM-scored.
	//
	// Note: stale/reject jobs that short-circuit BEFORE the LLM still get
	// scored legitimately (they don't consume budget); only LLM-needing jobs
	// are at risk. The cap is conservative — it limits the fetch, not just
	// the LLM call count. A larger sweep limit just under-utilizes, never
	// permanently strands jobs.
	maxLLM := score.MaxLLMPerCycle(deps.Settings)
	remaining := budget.acctRemaining(maxLLM)
	if remaining <= 0 {
		return
	}
	fetch := sweepLimit
	if remaining < fetch {
		fetch = remaining
	}

	jobs, err := store.UnscoredOpenJobs(ctx, fetch, rescoreAll)
	if err != nil {
		slog.WarnContext(ctx, "hunt worker: sweep UnscoredOpenJobs failed",
			slog.String("account", account), slog.Any("error", err))
		return
	}

	// ESC-2: set the ACCOUNT's unscored-jobs gauges from the sweep result (no
	// extra SQL query). Aggregate count and oldest first_seen_at in Go from
	// the UnscoredOpenJobs result (ordered ASC by first_seen_at).
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
	engine.SetHuntUnscoredJobsCount(account, count)
	engine.SetHuntUnscoredJobsMaxAge(account, maxAge)

	if len(jobs) == 0 {
		return
	}

	scored := 0
	for _, j := range jobs {
		sr := scoreJobWithLimit(ctx, hunt.OutcomeCreated, j, profile, deps, store, budget)
		if sr != nil {
			observeScore(*sr)
			scored++
		}
	}
	slog.InfoContext(ctx, "hunt worker: sweep complete",
		slog.String("account", account),
		slog.Int("swept", len(jobs)),
		slog.Int("scored", scored),
	)
}

// refreshUnscoredGauges updates gojob_hunt_unscored_jobs_count{account} and
// gojob_hunt_unscored_jobs_max_age_seconds{account} from a lightweight SQL
// query (COUNT + MIN(first_seen_at), no row fetch). Called by the periodic
// gauge refresher ticker between hunt cycles so the alert
// max(gojob_hunt_unscored_jobs_max_age_seconds) > 7200 reflects the LIVE
// per-account state, not a value frozen at the end of the last 6h cycle.
//
// Without this refresher, the gauge is set only inside runUnscoredSweep (once
// per 6h cycle) and stays frozen between cycles — a pipeline that stalls
// between cycles is invisible, and conversely a healthy pipeline with a
// 4h-old unscored job (ingested near the end of a cycle, not yet swept)
// triggers a false positive because the gauge hasn't been refreshed.
//
// No-op if the store doesn't satisfy unscoredJobStatsStore (test fake) or if
// the query fails (logged at WARN, gauges left at their last value — a query
// failure is not a pipeline stall).
func refreshUnscoredGauges(ctx context.Context, store any, account string) {
	statsStore, ok := store.(unscoredJobStatsStore)
	if !ok {
		return
	}
	stats, err := statsStore.UnscoredOpenJobsStats(ctx)
	if err != nil {
		slog.WarnContext(ctx, "hunt worker: gauge refresh UnscoredOpenJobsStats failed",
			slog.String("account", account), slog.Any("error", err))
		return
	}
	engine.SetHuntUnscoredJobsCount(account, float64(stats.Count))
	engine.SetHuntUnscoredJobsMaxAge(account, stats.OldestAge.Seconds())
}

// StartWorker starts the durable ingest worker in a background goroutine.
// The worker derives its account scope itself: every cycle enumerates ACTIVE
// panel_accounts LEFT JOIN account_hunt_settings (ADR-7) — accounts with no
// row or enabled=false are skipped; the worker needs no pre-resolved account.
//
// HUNT_INGEST_ENABLED=false is the explicit fleet kill-switch; when unset the
// worker still starts — the per-cycle enumeration decides (zero enabled
// accounts → cheap no-op cycles, or env-query corpus ingest when
// HUNT_INGEST_ENABLED=true).
//
// Must be called after engine.SetHuntStore.
// notifier may be nil — if nil, no Telegram notifications are sent by the
// worker. A *notify.ProductNotifier is cloned per account (chat id + recency
// gate from account_hunt_settings); other Notifier impls are reused as-is.
func StartWorker(ctx context.Context, store *hunt.Store, notifier hunt.Notifier) {
	// Nil-store first, on the CONCRETE pointer.
	if store == nil {
		slog.Debug("hunt worker: no store — skipping")
		return
	}
	if envEqualFold("HUNT_INGEST_ENABLED", "false") {
		slog.Debug("hunt worker: disabled (HUNT_INGEST_ENABLED=false)")
		return
	}
	w := NewWorker(store)
	if w == nil {
		slog.Warn("hunt worker: no store — skipping")
		return
	}
	w.SetNotifier(notifier)
	w.listAccounts = func(ctx context.Context) ([]accounts.AccountHuntSettings, error) {
		return accounts.ListAccountHuntSettings(ctx, store.Pool())
	}
	w.forAccount = func(aid uuid.UUID) accountScoreStore {
		return store.ForAccount(aid)
	}
	go w.Run(ctx)
}
