package hunt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// ErrArtifactJobNotFound is returned by MarkArtifactJob when the job (or the
// account) does not exist, so nothing could be marked.
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

// MarkArtifactJob records that this account generated application artifacts
// for a job, so retention keeps the job. It inserts a pipeline-stage "claimed"
// rating only when the account has NO rating row for the job: an existing row
// (any triage, stage or note) already protects the job and is never touched,
// so a higher stage the user set is never overwritten. When nothing was
// inserted and no rating exists either (unknown job or account) it returns
// ErrArtifactJobNotFound, so Persist cannot write files for a job nothing
// protects.
func (a *AccountStore) MarkArtifactJob(ctx context.Context, jobID int64) error {
	tag, err := a.s.pool.Exec(ctx, markArtifactSQL, []int64{jobID}, []uuid.UUID{a.aid})
	if err != nil {
		return fmt.Errorf("hunt: mark artifact job: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	var rated bool
	if err := a.s.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM hunt_ratings
		               WHERE entry_kind = 'job' AND entry_id = $1 AND account_id = $2)`,
		jobID, a.aid).Scan(&rated); err != nil {
		return fmt.Errorf("hunt: mark artifact job: probe rating: %w", err)
	}
	if !rated {
		return fmt.Errorf("%w: job %d, account %s", ErrArtifactJobNotFound, jobID, a.aid)
	}
	return nil
}

const markArtifactSQL = `
	INSERT INTO hunt_ratings (entry_kind, entry_id, account_id, stage, rated_at, updated_at)
	SELECT 'job', j.id, r.aid, 'claimed', NOW(), NOW()
	FROM unnest($1::bigint[], $2::uuid[]) AS r(jid, aid)
	JOIN hunt_jobs j ON j.id = r.jid
	JOIN panel_accounts p ON p.id = r.aid
	ON CONFLICT (entry_kind, entry_id, account_id) DO NOTHING`

// BackfillArtifactMarkers marks every job that already has artifacts so the
// prune cannot orphan them: jobs with an artifact directory, and, for the
// operator legacy slug directory (which has no job ids), every unrated job of
// a company that has a slug directory, whatever its age (a job live at boot
// would otherwise age out later and be pruned). Idempotent (DO NOTHING on an
// existing rating). Fails closed: an unreadable artifact store or legacy
// directory returns an error and marks nothing. Returns rows inserted.
func (s *Store) BackfillArtifactMarkers(ctx context.Context, idx ArtifactIndex) (int64, error) {
	aids, ids, err := idx.ArtifactJobRefs()
	if err != nil {
		return 0, fmt.Errorf("hunt: artifact backfill: %w", err)
	}
	artifactDirs := len(ids)

	match, legacyDirs, err := idx.LegacyMatcher()
	if err != nil {
		return 0, fmt.Errorf("hunt: artifact backfill: %w", err)
	}
	if owner := idx.LegacyOwner(); match != nil && owner != uuid.Nil {
		rows, err := s.pool.Query(ctx, `
			SELECT j.id, COALESCE(j.company, '')
			FROM hunt_jobs j
			WHERE NOT EXISTS (SELECT 1 FROM hunt_ratings r
			                  WHERE r.entry_kind = 'job' AND r.entry_id = j.id)`)
		if err != nil {
			return 0, fmt.Errorf("hunt: artifact backfill: list unrated jobs: %w", err)
		}
		for rows.Next() {
			var id int64
			var company string
			if err := rows.Scan(&id, &company); err != nil {
				rows.Close()
				return 0, fmt.Errorf("hunt: artifact backfill: scan: %w", err)
			}
			if match(company) {
				aids, ids = append(aids, owner), append(ids, id)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, fmt.Errorf("hunt: artifact backfill: unrated jobs: %w", err)
		}
	}
	var marked int64
	if len(ids) > 0 {
		tag, err := s.pool.Exec(ctx, markArtifactSQL, ids, aids)
		if err != nil {
			return 0, fmt.Errorf("hunt: artifact backfill: %w", err)
		}
		marked = tag.RowsAffected()
	}
	slog.Info("hunt retention: artifact marker backfill done",
		slog.Int("artifact_dirs", artifactDirs), slog.Int("legacy_dirs", legacyDirs),
		slog.Int("candidates", len(ids)), slog.Int64("marked", marked))
	return marked, nil
}
