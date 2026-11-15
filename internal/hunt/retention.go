package hunt

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/anatolykoptev/go-kit/env"
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
			tag, err := s.pool.Exec(ctx, q, retentionDays, pt.kind, batch)
			if err != nil {
				res.add(pt.table, total)
				return res, fmt.Errorf("hunt: prune %s: %w", pt.table, err)
			}
			n := tag.RowsAffected()
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
