package main

import (
	"context"
	"log/slog"

	"github.com/anatolykoptev/go_job/internal/engine/jobs/applications"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/google/uuid"
)

// startHuntRetention wires application_persist to mark its job (so retention
// keeps it), backfills markers for artifacts that already exist on disk, and
// only then starts the daily prune. A failed backfill leaves the prune off:
// pruning before every artifact job is marked would orphan its files.
func startHuntRetention(ctx context.Context, hs *hunt.Store, authority *applications.Authority) {
	if hs == nil {
		return
	}
	authority.SetJobMarker(func(ctx context.Context, aid uuid.UUID, jobID int64) error {
		return hs.ForAccount(aid).MarkArtifactJob(ctx, jobID)
	})
	if _, err := hs.BackfillArtifactMarkers(ctx, hunt.RetentionDays(), authority); err != nil {
		slog.Error("hunt retention: artifact marker backfill failed, prune NOT started", slog.Any("error", err))
		return
	}
	hs.StartRetention(ctx)
}
