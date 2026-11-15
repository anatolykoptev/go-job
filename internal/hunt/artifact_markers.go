package hunt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// Pin reasons stored in hunt_retention_pins.reason.
const (
	// PinArtifact: application artifacts exist for the job (keyed by job id).
	PinArtifact = "artifact"
	// PinLegacyCompany: the operator legacy slug directory covers the job's company.
	PinLegacyCompany = "legacy_company"
)

// ErrArtifactJobNotFound is returned by PinJob when the job does not exist, so
// nothing could be pinned.
var ErrArtifactJobNotFound = errors.New("hunt: artifact job not found")

// ArtifactIndex is the consumer-side view of the application artifact store
// that BackfillArtifactMarkers needs; *applications.Authority implements it.
// Every method fails closed: an unreadable store is an error, never "empty".
type ArtifactIndex interface {
	// ArtifactJobRefs lists accounts and job ids that have artifact
	// directories (parallel slices).
	ArtifactJobRefs() (accountIDs []uuid.UUID, jobIDs []int64, err error)
	// LegacyOwner is the account the pre-P4 slug directory belongs to.
	LegacyOwner() uuid.UUID
	// LegacyMatcher reports whether a company has a legacy slug directory;
	// nil match when legacy lookups are off. dirs counts the directories read.
	LegacyMatcher() (match func(company string) bool, dirs int, err error)
}

// pinSQL upserts pins for parallel (job id, reason) arrays. DISTINCT ON keeps
// one row per job (an artifact reason wins over legacy_company) because one
// ON CONFLICT DO UPDATE statement may not touch a row twice; an existing
// legacy_company pin is upgraded to artifact, never the reverse. Only existing
// jobs are pinned. Pins live in their own table: they never touch
// hunt_ratings, so the tracker and shortlist are unaffected.
const pinSQL = `
	INSERT INTO hunt_retention_pins (entry_kind, entry_id, reason)
	SELECT DISTINCT ON (j.id) 'job', j.id, r.reason
	FROM unnest($1::bigint[], $2::text[]) AS r(jid, reason)
	JOIN hunt_jobs j ON j.id = r.jid
	ORDER BY j.id, (r.reason = 'artifact') DESC
	ON CONFLICT (entry_kind, entry_id) DO UPDATE SET reason = EXCLUDED.reason
	WHERE hunt_retention_pins.reason = 'legacy_company' AND EXCLUDED.reason = 'artifact'`

// PinJob pins a job against the retention prune with the given reason
// (PinArtifact for Persist). Idempotent. Returns ErrArtifactJobNotFound when
// the job does not exist, so Persist cannot write files for a job nothing
// protects.
func (s *Store) PinJob(ctx context.Context, jobID int64, reason string) error {
	if _, err := s.pool.Exec(ctx, pinSQL, []int64{jobID}, []string{reason}); err != nil {
		return fmt.Errorf("hunt: pin job: %w", err)
	}
	var pinned bool
	if err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM hunt_retention_pins WHERE entry_kind = 'job' AND entry_id = $1)`,
		jobID).Scan(&pinned); err != nil {
		return fmt.Errorf("hunt: pin job: probe: %w", err)
	}
	if !pinned {
		return fmt.Errorf("%w: job %d", ErrArtifactJobNotFound, jobID)
	}
	return nil
}

// BackfillArtifactMarkers pins every job that already has artifacts so the
// prune cannot orphan them: PinArtifact for jobs with an account or legacy
// numeric artifact directory; PinLegacyCompany for every job (rated or not,
// any age) whose company has an operator legacy slug directory, since those
// directories carry no job id and a job live at boot would otherwise age out
// later. Idempotent. Fails closed: an unreadable artifact store or legacy
// directory returns an error and pins nothing. Returns rows written.
func (s *Store) BackfillArtifactMarkers(ctx context.Context, idx ArtifactIndex) (int64, error) {
	_, artifactIDs, err := idx.ArtifactJobRefs()
	if err != nil {
		return 0, fmt.Errorf("hunt: artifact backfill: %w", err)
	}
	ids := append([]int64(nil), artifactIDs...)
	reasons := make([]string, len(ids))
	for i := range reasons {
		reasons[i] = PinArtifact
	}

	match, legacyDirs, err := idx.LegacyMatcher()
	if err != nil {
		return 0, fmt.Errorf("hunt: artifact backfill: %w", err)
	}
	if match != nil && idx.LegacyOwner() != uuid.Nil {
		rows, err := s.pool.Query(ctx, `SELECT id, COALESCE(company, '') FROM hunt_jobs`)
		if err != nil {
			return 0, fmt.Errorf("hunt: artifact backfill: list jobs: %w", err)
		}
		for rows.Next() {
			var id int64
			var company string
			if err := rows.Scan(&id, &company); err != nil {
				rows.Close()
				return 0, fmt.Errorf("hunt: artifact backfill: scan: %w", err)
			}
			if match(company) {
				ids, reasons = append(ids, id), append(reasons, PinLegacyCompany)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, fmt.Errorf("hunt: artifact backfill: jobs: %w", err)
		}
	}
	var written int64
	if len(ids) > 0 {
		tag, err := s.pool.Exec(ctx, pinSQL, ids, reasons)
		if err != nil {
			return 0, fmt.Errorf("hunt: artifact backfill: %w", err)
		}
		written = tag.RowsAffected()
	}
	slog.Info("hunt retention: artifact pin backfill done",
		slog.Int("artifact_dirs", len(artifactIDs)), slog.Int("legacy_dirs", legacyDirs),
		slog.Int("candidates", len(ids)), slog.Int64("pinned", written))
	return written, nil
}
