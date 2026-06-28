package huntworker

import (
	"context"
	"log/slog"
	"os"
	"strings"
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
//   - fetchAllSecurityUnlimited → persistSecurity
//   - fetchAllBountiesUnlimited → persistBounties
//   - fetchAllFreelanceUnlimited → persistFreelanceJobs
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
// Each kind is fetched and persisted sequentially to limit peak memory on the
// 4-core krolik box; these are I/O-bound ops and do not need parallelism.
func (w *OppWorker) runCycle(ctx context.Context) {
	start := time.Now()
	slog.Info("opp worker: cycle start")

	// Security programs (BTD 5 sources + Immunefi + Sherlock + Cantina + Code4rena)
	secPrograms := jobs.FetchAllSecurityUnlimited(ctx)
	jobs.PersistSecurity(ctx, secPrograms)

	// Bounties (Algora + Opire + BountyHub + Boss + Lightning + Collaborators)
	bounties := jobs.FetchAllBountiesUnlimited(ctx)
	jobs.PersistBounties(ctx, bounties)

	// Freelance (RemoteOK + Himalayas)
	freelance := jobs.FetchAllFreelanceUnlimited(ctx)
	jobs.PersistFreelanceJobs(ctx, freelance)

	slog.Info("opp worker: cycle complete",
		slog.Duration("elapsed", time.Since(start)),
		slog.Int("security", len(secPrograms)),
		slog.Int("bounties", len(bounties)),
		slog.Int("freelance", len(freelance)),
	)
}

// huntOppIngestEnabled reads the HUNT_OPP_INGEST_ENABLED env flag.
// Defaults to true — operator wants it on.
func huntOppIngestEnabled() bool {
	v := strings.TrimSpace(os.Getenv("HUNT_OPP_INGEST_ENABLED"))
	if v == "" {
		return true // default on
	}
	return strings.EqualFold(v, "true")
}

// StartOpportunityWorker starts the opportunity ingest worker in a background
// goroutine when HUNT_OPP_INGEST_ENABLED is true (default true) and the hunt
// store is available (non-nil — DB configured). Noop otherwise.
// Must be called after engine.SetHuntStore.
func StartOpportunityWorker(ctx context.Context, store *hunt.Store) {
	if !huntOppIngestEnabled() {
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
