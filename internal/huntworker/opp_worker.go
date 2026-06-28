package huntworker

import (
	"context"
	"log/slog"
	"time"

	"github.com/anatolykoptev/go-kit/env"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/anatolykoptev/go_job/internal/hunt"
)

// OppWorker runs a periodic ingest cycle for security programs, bounties, and
// freelance projects. Separate from the ATS Worker (job boards) so each runs
// on its own interval without coupling.
//
// Gate: HUNT_OPP_INGEST_ENABLED (default true).
// Interval: HUNT_OPP_INGEST_INTERVAL (default 12h).
//
// On each cycle it calls the unlimited-fetch helpers in engine/jobs:
//   - FetchAllSecurityUnlimited → PersistSecurity
//   - FetchAllBountiesUnlimited → PersistBounties
//   - FetchAllFreelanceUnlimited → PersistFreelanceJobs
//
// The persist functions own the notify policy (backfill guard + isUrgent gate)
// so both the scheduled and on-demand paths share identical behaviour.
type OppWorker struct {
	interval time.Duration
}

// NewOppWorker builds an OppWorker from env vars.
func NewOppWorker() *OppWorker {
	return &OppWorker{
		interval: env.Duration("HUNT_OPP_INGEST_INTERVAL", 12*time.Hour),
	}
}

// Run blocks until ctx is cancelled, firing a cycle every interval.
// Runs one cycle immediately at startup so the DB is populated before the
// first tick (mirrors the ATS Worker.Run pattern).
func (w *OppWorker) Run(ctx context.Context) {
	slog.Info("opp worker: starting", slog.Duration("interval", w.interval))

	w.runCycle(ctx)

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("opp worker: stopping")
			return
		case <-ticker.C:
			w.runCycle(ctx)
		}
	}
}

// runCycle executes one full ingest cycle for all three opportunity kinds.
// Each kind runs in its own closure with a defer-recover so a panic in one
// source (e.g. a malformed parser response) cannot abort the remaining kinds
// or prevent the next scheduled cycle. Mirrors the per-platform panic isolation
// in the ATS Worker.
func (w *OppWorker) runCycle(ctx context.Context) {
	start := time.Now()
	slog.Info("opp worker: cycle start")

	// Security programs (BTD 5 sources + Immunefi + Sherlock + Cantina + Code4rena)
	func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("opp worker: recovered panic",
					slog.String("kind", "security"),
					slog.Any("panic", r),
				)
			}
		}()
		secPrograms := jobs.FetchAllSecurityUnlimited(ctx)
		jobs.PersistSecurity(ctx, secPrograms)
	}()

	// Bounties (Algora + Opire + BountyHub + Boss + Lightning + Collaborators)
	func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("opp worker: recovered panic",
					slog.String("kind", "bounty"),
					slog.Any("panic", r),
				)
			}
		}()
		bounties := jobs.FetchAllBountiesUnlimited(ctx)
		jobs.PersistBounties(ctx, bounties)
	}()

	// Freelance (RemoteOK + Himalayas)
	func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("opp worker: recovered panic",
					slog.String("kind", "freelance"),
					slog.Any("panic", r),
				)
			}
		}()
		freelance := jobs.FetchAllFreelanceUnlimited(ctx)
		jobs.PersistFreelanceJobs(ctx, freelance)
	}()

	slog.Info("opp worker: cycle complete",
		slog.Duration("elapsed", time.Since(start)),
	)
}

// StartOpportunityWorker starts the opportunity ingest worker in a background
// goroutine when HUNT_OPP_INGEST_ENABLED is true (default true) and the hunt
// store is available (non-nil — DB configured). Noop otherwise.
// Must be called after engine.SetHuntStore.
func StartOpportunityWorker(ctx context.Context, store *hunt.Store) {
	if !env.Bool("HUNT_OPP_INGEST_ENABLED", true) {
		slog.Debug("opp worker: disabled (HUNT_OPP_INGEST_ENABLED=false)")
		return
	}
	if store == nil {
		slog.Warn("opp worker: no store — skipping")
		return
	}
	w := NewOppWorker()
	go w.Run(ctx)
}
