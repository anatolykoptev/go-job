package main

import (
	"context"
	"log/slog"
	"sync/atomic"

	"github.com/anatolykoptev/go_job/internal/accounts"
	"github.com/anatolykoptev/go_job/internal/engine/jobs/applications"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// huntFKReady is set once the account_job_scores -> hunt_jobs FK and its
// job_id index are in place (main.go, after hunt Migrate). The prune relies on
// the cascade; without it, it would orphan scores and seq-scan under its lock.
var huntFKReady atomic.Bool

// ensureHuntFK installs the account_job_scores FK + index (no-op when present).
// Not boot-fatal: orphan scores are inert and the next boot retries. A failure
// leaves huntFKReady false, so the prune is skipped in this process.
func ensureHuntFK(ctx context.Context, pool *pgxpool.Pool) {
	if _, err := accounts.EnsureJobScoresJobFK(ctx, pool); err != nil {
		slog.Warn("account_job_scores FK step failed; continuing without retention prune", slog.Any("error", err))
		return
	}
	huntFKReady.Store(true)
}

// startHuntRetention wires application_persist to pin its job (so retention
// keeps it) and starts the retention loop, which backfills markers for
// artifacts that already exist before its first prune. HUNT_RETENTION_DAYS is
// read once, here: 0 skips everything (no marker, no backfill, no prune). So
// does fkReady == false (the FK/index step failed at boot).
func startHuntRetention(ctx context.Context, hs *hunt.Store, authority *applications.Authority, fkReady bool) {
	if hs == nil {
		return
	}
	days := hunt.RetentionDays()
	if days > 0 && !fkReady {
		slog.Error("hunt retention: NOT started in this process: account_job_scores FK/index step failed at boot; fix it and restart")
		return
	}
	wireArtifactMarker(authority, func(ctx context.Context, _ uuid.UUID, jobID int64) error {
		return hs.PinJob(ctx, jobID, hunt.PinArtifact)
	}, days)
	hs.StartRetention(ctx, authority)
}

// wireArtifactMarker installs mark on the authority and, because retention is
// on, makes Persist fail loudly if the marker is ever missing. days == 0 wires
// nothing.
func wireArtifactMarker(authority *applications.Authority, mark applications.JobMarker, days int) {
	if days <= 0 {
		return
	}
	authority.RequireJobMarker()
	authority.SetJobMarker(mark)
}
