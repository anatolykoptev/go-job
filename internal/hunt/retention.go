package hunt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/anatolykoptev/go-kit/env"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Retention of the shared hunt corpus. Postings are re-crawled from the ATS
// boards, so a row that has not been seen for a long time is regenerable
// garbage; a row a user acted on is not and is never pruned.
const (
	// DefaultRetentionDays is the HUNT_RETENTION_DAYS default.
	DefaultRetentionDays = 90

	// minRetentionDays floors a non-zero HUNT_RETENTION_DAYS. Security rows
	// refresh last_seen_at at most once per securityLastSeenRefresh, so a
	// retention at or below it would delete live rows.
	minRetentionDays = 7

	// pruneBatchSize bounds one DELETE statement (and so its lock footprint and
	// WAL burst) on a first run that has thousands of rows to remove.
	pruneBatchSize = 1000

	// retentionInterval is the prune cadence.
	retentionInterval = 24 * time.Hour
)

// retentionLockKey is the transaction-level advisory lock that orders the
// prune against every writer of hunt_ratings and hunt_retention_pins.
//
// The prune's NOT EXISTS sees only the snapshot of its own DELETE statement
// (READ COMMITTED), so a rating or pin committed while a batch runs is
// invisible to it and the job could be deleted. Instead each prune batch takes
// the lock EXCLUSIVE before its DELETE and each writer takes it SHARED inside
// its own transaction before writing, held to commit:
//   - a writer in flight: the batch waits for its commit, then its DELETE
//     (statement start is after the lock) sees the rating or pin;
//   - a batch in flight: the writer waits for the batch to commit.
//
// Writers never block each other, only a running batch (milliseconds). A
// writer that lands just after a batch deleted its job writes a rating for a
// job that no longer exists; nothing can prevent that, the job was already gone.
const retentionLockKey int64 = 0x68756e7472657431 // "huntret1"

// execRetentionShared runs one write to hunt_ratings or hunt_retention_pins in
// a transaction holding the shared retention lock. Every such writer must use
// it (or lockRetentionShared inside its own tx).
func (s *Store) execRetentionShared(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockRetentionShared(ctx, tx); err != nil {
		return pgconn.CommandTag{}, err
	}
	tag, err := tx.Exec(ctx, sql, args...)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	return tag, tx.Commit(ctx)
}

// lockRetentionShared takes the shared retention lock until tx ends.
func lockRetentionShared(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, retentionLockKey)
	return err
}

// pruneLockTimeout bounds how long one batch may wait, holding the exclusive
// retention lock, on a row lock its DELETE needs (or on the advisory lock
// itself). Writers queue behind the batch meanwhile, so it must stay short.
const pruneLockTimeout = "5s"

// errPruneLockTimeout marks a batch abandoned on lock_timeout (SQLSTATE 55P03).
var errPruneLockTimeout = errors.New("hunt: prune batch lock timeout")

// pruneBatch deletes one batch under the exclusive retention lock.
func (s *Store) pruneBatch(ctx context.Context, q string, args ...any) (n int64, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	defer func() {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
			n, err = 0, errPruneLockTimeout
		}
	}()
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '`+pruneLockTimeout+`'`); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, retentionLockKey); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), tx.Commit(ctx)
}

// PruneResult counts the rows PruneStale removed per table.
type PruneResult struct {
	Jobs      int64
	Freelance int64
	Security  int64
}

// Total is the number of rows removed across all tables.
func (r PruneResult) Total() int64 { return r.Jobs + r.Freelance + r.Security }

// prunedTables lists the tables the retention prune covers with the
// hunt_ratings.entry_kind that marks a row as acted on. hunt_bounties is
// excluded on purpose: it has its own liveness checker
// (GetBountiesNeedingCheck) and closes rows itself.
var prunedTables = []struct{ table, kind string }{
	{"hunt_jobs", KindJob},
	{"hunt_freelance", KindFreelance},
	{"hunt_security", KindSecurity},
}

// pruneSQLTemplate deletes one bounded batch of stale rows that no account has
// rated or pinned. Pinned = a hunt_retention_pins row (application artifacts
// exist for the job, or its company has a legacy application directory); pins
// are invisible to the tracker. "Acted on" is any hunt_ratings row for the entry, whatever its
// account, triage (interesting/saved/discarded), pipeline stage
// (claimed/applied/interview/offer/rejected) or note: the star toggle, the
// triage buttons, the stage dropdown and the tracker tool all write that one
// table. account_job_scores rows are derived (LLM fit score) and carry no user
// signal; they cascade away with the job (FK in accounts.EnsureJobScoresJobFK).
const pruneSQLTemplate = `
	DELETE FROM {table} d WHERE d.id IN (
		SELECT t.id FROM {table} t
		WHERE t.last_seen_at < NOW() - make_interval(days => $1::int)
		  AND NOT EXISTS (
		      SELECT 1 FROM hunt_ratings r
		      WHERE r.entry_kind = $2 AND r.entry_id = t.id)
		  AND NOT EXISTS (
		      SELECT 1 FROM hunt_retention_pins p
		      WHERE p.entry_kind = $2 AND p.entry_id = t.id)
		ORDER BY t.last_seen_at
		LIMIT $3)
	  -- re-checked at delete time: a row refreshed or rated after the subquery
	  -- picked it must not be deleted.
	  AND d.last_seen_at < NOW() - make_interval(days => $1::int)
	  AND NOT EXISTS (
	      SELECT 1 FROM hunt_ratings r
	      WHERE r.entry_kind = $2 AND r.entry_id = d.id)
	  AND NOT EXISTS (
	      SELECT 1 FROM hunt_retention_pins p
	      WHERE p.entry_kind = $2 AND p.entry_id = d.id)`

// RetentionDays reads HUNT_RETENTION_DAYS: default 90, 0 or negative disables
// the prune, a positive value below minRetentionDays is raised to it.
func RetentionDays() int {
	days := env.Int("HUNT_RETENTION_DAYS", DefaultRetentionDays)
	switch {
	case days <= 0:
		return 0
	case days < minRetentionDays:
		slog.Warn("hunt retention: HUNT_RETENTION_DAYS below floor, raised",
			slog.Int("configured", days), slog.Int("effective", minRetentionDays))
		return minRetentionDays
	}
	return days
}

// PruneStale deletes jobs, freelance projects and security programs not seen
// for retentionDays days that no account has rated. retentionDays <= 0 is a
// no-op. Deletes run in bounded batches; on error the counts so far are
// returned alongside it.
func (s *Store) PruneStale(ctx context.Context, retentionDays int) (PruneResult, error) {
	return s.PruneStaleBatch(ctx, retentionDays, pruneBatchSize)
}

// PruneStaleBatch is PruneStale with an explicit DELETE batch size (exported
// so tests can drive the multi-batch loop with a handful of rows).
func (s *Store) PruneStaleBatch(ctx context.Context, retentionDays, batch int) (PruneResult, error) {
	var res PruneResult
	if retentionDays <= 0 || batch <= 0 {
		return res, nil
	}
	for _, pt := range prunedTables {
		q := strings.ReplaceAll(pruneSQLTemplate, "{table}", pt.table)
		var total int64
		for {
			n, err := s.pruneBatch(ctx, q, retentionDays, pt.kind, batch)
			if errors.Is(err, errPruneLockTimeout) {
				// A row the batch needs is locked (an upsert or a user write in
				// flight). Not a failure: end this run, the next cycle retries.
				// Deliberately not counted in hunt_retention_errors_total (alerted
				// on increase > 0); the Info line is the signal.
				slog.Info("hunt retention: batch hit lock timeout, run ended; next cycle retries",
					slog.String("table", pt.table), slog.String("lock_timeout", pruneLockTimeout))
				res.add(pt.table, total)
				return res, nil
			}
			if err != nil {
				res.add(pt.table, total)
				return res, fmt.Errorf("hunt: prune %s: %w", pt.table, err)
			}
			total += n
			if n < int64(batch) {
				break
			}
		}
		res.add(pt.table, total)
	}
	return res, nil
}

func (r *PruneResult) add(table string, n int64) {
	switch table {
	case "hunt_jobs":
		r.Jobs += n
	case "hunt_freelance":
		r.Freelance += n
	case "hunt_security":
		r.Security += n
	}
}

var (
	onRetentionPruned func(kind string, n int64)
	onRetentionError  func()
)

// SetRetentionMetricHooks wires the retention metric callbacks (avoids an
// engine import cycle, same shape as the oversize purge hooks). Called from
// main before StartRetention.
func SetRetentionMetricHooks(prunedFn func(kind string, n int64), errorFn func()) {
	onRetentionPruned = prunedFn
	onRetentionError = errorFn
}

// retentionBackfillBackoff is the first retry delay after a failed artifact
// marker backfill; it doubles up to 32x.
const retentionBackfillBackoff = time.Minute

// StartRetention starts the retention loop in the background: first the
// artifact marker backfill (retried with backoff until it succeeds, each
// failure counted in hunt_retention_errors_total), then the prune once and
// every 24h. HUNT_RETENTION_DAYS is read once, here, at boot: 0 disables the
// loop entirely, backfill included; changing it needs a restart.
func (s *Store) StartRetention(ctx context.Context, idx ArtifactIndex) {
	if s == nil {
		return
	}
	days := RetentionDays()
	if days == 0 {
		slog.Info("hunt retention: disabled (HUNT_RETENTION_DAYS=0)")
		return
	}
	go s.RunRetentionLoop(ctx, days, idx, retentionBackfillBackoff)
}

// RunRetentionLoop blocks until ctx is done. The prune never runs before the
// backfill has succeeded: pruning with an unmarked artifact job would orphan
// its files. Exported for tests.
func (s *Store) RunRetentionLoop(ctx context.Context, days int, idx ArtifactIndex, backoff time.Duration) {
	delay := backoff
	for {
		if _, err := s.BackfillArtifactMarkers(ctx, idx); err == nil {
			break
		} else {
			if onRetentionError != nil {
				onRetentionError()
			}
			slog.Error("hunt retention: artifact marker backfill failed, prune NOT started; retrying",
				slog.Any("error", err), slog.Duration("retry_in", delay))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, backoff*32)
	}
	s.runRetention(ctx, days)
	ticker := time.NewTicker(retentionInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runRetention(ctx, days)
		}
	}
}

func (s *Store) runRetention(ctx context.Context, days int) {
	start := time.Now()
	res, err := s.PruneStale(ctx, days)
	if onRetentionPruned != nil {
		onRetentionPruned(KindJob, res.Jobs)
		onRetentionPruned(KindFreelance, res.Freelance)
		onRetentionPruned(KindSecurity, res.Security)
	}
	if err != nil {
		if onRetentionError != nil {
			onRetentionError()
		}
		slog.Warn("hunt retention: prune failed", slog.Any("error", err),
			slog.Int64("jobs", res.Jobs), slog.Int64("freelance", res.Freelance),
			slog.Int64("security", res.Security))
		return
	}
	slog.Info("hunt retention: pruned",
		slog.Int("retention_days", days),
		slog.Int64("jobs", res.Jobs), slog.Int64("freelance", res.Freelance),
		slog.Int64("security", res.Security),
		slog.Duration("took", time.Since(start)))
}
