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
// rated. "Acted on" is any hunt_ratings row for the entry, whatever its
// account, triage (interesting/saved/discarded), pipeline stage
// (claimed/applied/interview/offer/rejected) or note: the star toggle, the
// triage buttons, the stage dropdown and the tracker tool all write that one
// table. account_job_scores rows are derived (LLM fit score) and carry no user
// signal; they cascade away with the job (FK in accounts.EnsureJobScoresJobFK).
const pruneSQLTemplate = `
	DELETE FROM {table} WHERE id IN (
		SELECT t.id FROM {table} t
		WHERE t.last_seen_at < NOW() - make_interval(days => $1::int)
		  AND NOT EXISTS (
		      SELECT 1 FROM hunt_ratings r
		      WHERE r.entry_kind = $2 AND r.entry_id = t.id)
		ORDER BY t.last_seen_at
		LIMIT $3)`

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

// StartRetention runs the prune once now and then every 24h until ctx is done.
// HUNT_RETENTION_DAYS=0 disables it.
func (s *Store) StartRetention(ctx context.Context) {
	if s == nil {
		return
	}
	days := RetentionDays()
	if days == 0 {
		slog.Info("hunt retention: disabled (HUNT_RETENTION_DAYS=0)")
		return
	}
	go func() {
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
	}()
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
