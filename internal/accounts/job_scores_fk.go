package accounts

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// jobScoresFKName is the constraint EnsureJobScoresJobFK installs.
const jobScoresFKName = "account_job_scores_job_id_fkey"

// EnsureJobScoresJobFK adds account_job_scores.job_id -> hunt_jobs(id) ON
// DELETE CASCADE, so pruning or deleting a job removes its scores instead of
// orphaning them. Orphan scores (job_id with no hunt_jobs row) are deleted
// first and counted, otherwise the constraint cannot be added.
//
// The FK cannot live in account_job_scores.sql: Bootstrap runs BEFORE
// hStore.Migrate, so on a fresh DB hunt_jobs does not exist yet (and the
// schema sweep in CI applies hunt DDL standalone). Same probe-and-skip shape
// as BackfillLegacyJobScores: absent tables or an existing constraint return
// (0, nil). Bootstrap calls it on every boot, and main calls it again right
// after hStore.Migrate so a fresh DB gets the FK on its first boot.
//
// Both tables are small, so the FK is added plain (no NOT VALID/VALIDATE);
// the lock on account_job_scores spans the orphan delete and the ADD so a
// concurrent score write cannot slip an orphan in between.
func EnsureJobScoresJobFK(ctx context.Context, pool *pgxpool.Pool) (orphans int64, err error) {
	var scores, jobs *string
	if err := pool.QueryRow(ctx,
		`SELECT to_regclass('public.account_job_scores')::text, to_regclass('public.hunt_jobs')::text`,
	).Scan(&scores, &jobs); err != nil {
		return 0, fmt.Errorf("probe tables: %w", err)
	}
	if scores == nil || jobs == nil {
		return 0, nil
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Never wedge boot behind a long transaction: give up after 5s (the caller
	// logs and continues; the next boot retries).
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '5s'`); err != nil {
		return 0, fmt.Errorf("set lock_timeout: %w", err)
	}
	if _, err := tx.Exec(ctx, `LOCK TABLE account_job_scores IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return 0, fmt.Errorf("lock account_job_scores: %w", err)
	}
	// ON DELETE CASCADE seq-scans account_job_scores once per deleted job
	// without this index (the UNIQUE key leads with account_id).
	if _, err := tx.Exec(ctx,
		`CREATE INDEX IF NOT EXISTS account_job_scores_job_id_idx ON account_job_scores (job_id)`); err != nil {
		return 0, fmt.Errorf("create job_id index: %w", err)
	}
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM pg_constraint
		               WHERE conname = $1 AND conrelid = 'public.account_job_scores'::regclass)`,
		jobScoresFKName).Scan(&exists); err != nil {
		return 0, fmt.Errorf("probe constraint: %w", err)
	}
	if exists {
		return 0, nil
	}
	tag, err := tx.Exec(ctx, `
		DELETE FROM account_job_scores s
		WHERE NOT EXISTS (SELECT 1 FROM hunt_jobs j WHERE j.id = s.job_id)`)
	if err != nil {
		return 0, fmt.Errorf("delete orphan scores: %w", err)
	}
	orphans = tag.RowsAffected()
	if _, err := tx.Exec(ctx, `
		ALTER TABLE account_job_scores
		ADD CONSTRAINT `+jobScoresFKName+`
		FOREIGN KEY (job_id) REFERENCES hunt_jobs(id) ON DELETE CASCADE`); err != nil {
		return 0, fmt.Errorf("add foreign key: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	slog.Info("accounts: account_job_scores.job_id FK installed",
		slog.Int64("orphan_scores_deleted", orphans))
	return orphans, nil
}
