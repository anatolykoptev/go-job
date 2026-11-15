package hunt

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// ArtifactIndex is the consumer-side view of the application artifact store
// that BackfillArtifactMarkers needs; *applications.Authority implements it.
type ArtifactIndex interface {
	// ArtifactJobRefs lists accounts and job ids that have artifact
	// directories (parallel slices).
	ArtifactJobRefs() (accountIDs []uuid.UUID, jobIDs []int64)
	// LegacyOwner is the account the pre-P4 slug directory belongs to.
	LegacyOwner() uuid.UUID
	// LegacyMatcher reports whether the legacy slug directory has a PDF for a
	// company and title; nil when legacy lookups are off.
	LegacyMatcher() func(company, title string) bool
}

// MarkArtifactJob records that this account generated application artifacts
// for a job, so retention keeps the job. It inserts a pipeline-stage "claimed"
// rating only when the account has NO rating row for the job: an existing row
// (any triage, stage or note) already protects the job and is never touched,
// so a higher stage the user set is never overwritten. A job id that is not in
// hunt_jobs inserts nothing.
func (a *AccountStore) MarkArtifactJob(ctx context.Context, jobID int64) error {
	_, err := a.s.pool.Exec(ctx, markArtifactSQL, []int64{jobID}, []uuid.UUID{a.aid})
	if err != nil {
		return fmt.Errorf("hunt: mark artifact job: %w", err)
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

// BackfillArtifactMarkers marks every job that already has artifacts on disk
// but no rating, so the first prune cannot orphan them. Idempotent (DO
// NOTHING on an existing rating) and cheap: directory names plus one INSERT,
// and for the operator's legacy slug directory a scan of only the jobs the
// prune would delete (unseen for retentionDays and unrated). Returns the
// number of rows inserted. main runs it before StartRetention and does not
// start the prune when it fails.
func (s *Store) BackfillArtifactMarkers(ctx context.Context, retentionDays int, idx ArtifactIndex) (int64, error) {
	aids, ids := idx.ArtifactJobRefs()

	if match, owner := idx.LegacyMatcher(), idx.LegacyOwner(); match != nil && owner != uuid.Nil && retentionDays > 0 {
		rows, err := s.pool.Query(ctx, `
			SELECT j.id, COALESCE(j.company, ''), COALESCE(j.title, '')
			FROM hunt_jobs j
			WHERE j.last_seen_at < NOW() - make_interval(days => $1::int)
			  AND NOT EXISTS (SELECT 1 FROM hunt_ratings r
			                  WHERE r.entry_kind = 'job' AND r.entry_id = j.id)`, retentionDays)
		if err != nil {
			return 0, fmt.Errorf("hunt: artifact backfill: list prune candidates: %w", err)
		}
		for rows.Next() {
			var id int64
			var company, title string
			if err := rows.Scan(&id, &company, &title); err != nil {
				rows.Close()
				return 0, fmt.Errorf("hunt: artifact backfill: scan: %w", err)
			}
			if match(company, title) {
				aids, ids = append(aids, owner), append(ids, id)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, fmt.Errorf("hunt: artifact backfill: candidates: %w", err)
		}
	}
	if len(ids) == 0 {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, markArtifactSQL, ids, aids)
	if err != nil {
		return 0, fmt.Errorf("hunt: artifact backfill: %w", err)
	}
	if n := tag.RowsAffected(); n > 0 {
		slog.Info("hunt retention: marked jobs that have application artifacts",
			slog.Int64("marked", n), slog.Int("artifact_jobs", len(ids)))
	}
	return tag.RowsAffected(), nil
}
