package main

import (
	"context"

	"github.com/anatolykoptev/go_job/internal/engine/jobs/applications"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/google/uuid"
)

// startHuntRetention wires application_persist to mark its job (so retention
// keeps it) and starts the retention loop, which backfills markers for
// artifacts that already exist before its first prune. HUNT_RETENTION_DAYS is
// read once, here: 0 skips everything (no marker, no backfill, no prune).
func startHuntRetention(ctx context.Context, hs *hunt.Store, authority *applications.Authority) {
	if hs == nil {
		return
	}
	days := hunt.RetentionDays()
	wireArtifactMarker(authority, func(ctx context.Context, aid uuid.UUID, jobID int64) error {
		return hs.ForAccount(aid).MarkArtifactJob(ctx, jobID)
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
