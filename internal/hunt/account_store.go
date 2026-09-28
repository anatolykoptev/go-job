package hunt

// account_store.go — the account-scoped data-plane facade (plan ADR-15).
//
// Per-account judgment data (account_job_scores, hunt_ratings,
// account_hunt_settings) is reachable ONLY through this type: the unscoped
// statement shape is deliberately absent from *Store so "forgot the WHERE
// account_id" is not an expressible bug — the account id is bound once at
// ForAccount and every query below carries its predicate. Omission is
// compile-loud: a caller without an account cannot even construct the call.
//
// The shared hunt_* corpus (hunt_jobs rows themselves, freelance, security,
// settings) stays on *Store — it is global by design (ADR-6).
//
// P3: hunt_ratings.account_id replaced user_name as the scoping key — the
// column itself stays until the post-soak drop (ADR-13) but is dead to all
// reads and writes from this phase on.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/anatolykoptev/go_job/internal/accounts"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// AccountStore is the account-scoped view of the hunt store. Obtain it via
// (*Store).ForAccount — never construct it directly.
type AccountStore struct {
	s   *Store // back-pointer: corpus reads still use the parent's pool/enricher
	aid uuid.UUID
}

// ForAccount binds a per-account facade for account-scoped reads and writes.
// The caller must have resolved aid from the request's verified identity
// (accounts.AccountFrom / the pinned operator under AUTH_DRIVER=hmac);
// uuid.Nil is accepted but fails loud at write time (account_id FK) and reads
// match nothing — there is no "all accounts" fast path through this type.
func (s *Store) ForAccount(aid uuid.UUID) *AccountStore {
	return &AccountStore{s: s, aid: aid}
}

// AccountID returns the account this facade is bound to.
func (a *AccountStore) AccountID() uuid.UUID { return a.aid }

// ShortlistRow is the postgres projection for the /admin/shortlist page.
// It joins hunt_jobs with hunt_ratings for the bound account, filtered to the
// active triage+stage sets. All nullable DB columns use pointer types.
type ShortlistRow struct {
	ID             int64
	Title          string
	Company        string
	URL            string
	Location       string
	FitScore       *int
	FitBand        string
	SuccessBand    string
	OverUnder      string
	SalaryMin      *int
	SalaryMax      *int
	SalaryCurrency string
	SalaryInterval string
	PostedAt       *time.Time
	ScoredAt       *time.Time
	Triage         string // '' = untriaged
	Stage          string // '' = not in pipeline
	Note           string
	RatedAt        time.Time
}

// ShortlistQuery is the parameter bag for AccountStore.ListShortlist.
//
// The caller renders FilterSpec → WhereConds/WhereArgs (admintable.FilterSpec.Where)
// and Spec.OrderBy → OrderBy (admintable.Spec.OrderBy) before calling. This keeps
// the hunt package free of admintable imports while allowing the lister and tests to
// share a single query path — so the isolation test guards the live code path.
type ShortlistQuery struct {
	// TriageValues is the set of hunt_ratings.triage values that qualify a job for the shortlist.
	// A job appears if EITHER r.triage = ANY(TriageValues) OR r.stage = ANY(StageValues).
	TriageValues []string
	// StageValues is the set of hunt_ratings.stage values that qualify a job for the shortlist.
	StageValues []string
	// WhereConds is the pre-rendered SQL boolean expression from admintable.FilterSpec.Where.
	// Bind arguments are in WhereArgs ($1…$N). Empty → treated as "TRUE".
	WhereConds string
	WhereArgs  []any
	// OrderBy is the pre-rendered ORDER BY clause (column list, no keyword) from
	// admintable.Spec.OrderBy. Empty → default: "s.fit_score DESC NULLS LAST, j.company".
	OrderBy string
	// Limit and Offset control pagination. Limit=0 → fetch all (no LIMIT clause).
	Limit  int
	Offset int
}

// UnscoredJobsStats holds the aggregate stats for the unscored-open pool.
// Used by the periodic gauge refresher to update gojob_hunt_unscored_jobs_count
// and gojob_hunt_unscored_jobs_max_age_seconds between hunt cycles without
// fetching full job rows.
type UnscoredJobsStats struct {
	Count     int
	OldestAge time.Duration // zero if Count == 0
}

// SetJobScore persists the fit-scoring result for a job UNDER THIS ACCOUNT —
// an UPSERT into account_job_scores keyed (account_id, job_id), so re-scoring
// rewrites this account's row only and can never touch a foreign account's
// score. Replaces the old UPDATE hunt_jobs SET fit_* write (ADR-6/ADR-15);
// the global columns are dead to reads and writes.
//
// The job row must exist in the shared hunt_jobs corpus — enforced by the
// WHERE EXISTS probe (account_job_scores.job_id carries no FK: Bootstrap
// precedes hStore.Migrate, see account_job_scores.sql). A missing job returns
// ErrNotFound, same contract the hunt_jobs UPDATE had.
//
// It writes ONLY score columns. It does not touch job status, closed_at,
// first_seen_at, or any ingest fields — scoring is orthogonal to ingest.
func (a *AccountStore) SetJobScore(ctx context.Context, id int64, sr ScoreResult) error {
	rationale := scoreRationale{
		FitReasons:       sr.FitReasons,
		FitGaps:          sr.FitGaps,
		SuccessReasoning: sr.SuccessReasoning,
	}
	rationaleJSON, err := json.Marshal(rationale)
	if err != nil {
		return fmt.Errorf("hunt: marshal score rationale: %w", err)
	}
	ct, err := a.s.pool.Exec(ctx, `
		INSERT INTO account_job_scores
			(account_id, job_id, fit_score, fit_band, success_band, over_under, score_rationale, scored_at)
		SELECT $1, j.id, $3, $4, $5, $6, $7, $8
		FROM hunt_jobs j
		WHERE j.id = $2
		ON CONFLICT (account_id, job_id) DO UPDATE
		SET fit_score       = EXCLUDED.fit_score,
		    fit_band        = EXCLUDED.fit_band,
		    success_band    = EXCLUDED.success_band,
		    over_under      = EXCLUDED.over_under,
		    score_rationale = EXCLUDED.score_rationale,
		    scored_at       = EXCLUDED.scored_at`,
		a.aid, id,
		sr.FitScore, sr.FitBand, sr.SuccessBand, sr.OverUnder,
		rationaleJSON, sr.ScoredAt,
	)
	if err != nil {
		return fmt.Errorf("hunt: set job score: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("hunt: set job score: %w", ErrNotFound)
	}
	return nil
}

// UnscoredOpenJobs returns open jobs that have never been scored FOR THIS
// ACCOUNT (no account_job_scores row), oldest first, capped at limit. When
// rescoreAll is true the score-existence filter is dropped (re-score
// everything open) for the HUNT_SCORE_RESCORE_ALL one-shot.
//
// The "unscored" predicate is per-account: a job scored by account A is
// still unscored for B — each account's sweep drains its own pool.
func (a *AccountStore) UnscoredOpenJobs(ctx context.Context, limit int, rescoreAll bool) ([]Job, error) {
	limit = clampLimit(limit, 50, 500)
	rows, err := a.s.pool.Query(ctx, `
		SELECT j.id, j.dedup_hash, j.title, j.company, j.url, j.source, j.external_id, j.location, j.remote,
		       j.job_type, j.experience, j.salary_min, j.salary_max, j.salary_currency, j.salary_interval,
		       j.skills, j.tags, j.description, j.posted_at, j.first_seen_at, j.last_seen_at,
		       j.status, j.closed_at, j.last_checked_at
		FROM hunt_jobs j
		WHERE j.status = 'open'
		  AND (NOT EXISTS (
		        SELECT 1 FROM account_job_scores s
		        WHERE s.account_id = $2 AND s.job_id = j.id
		      ) OR $3)
		ORDER BY j.first_seen_at ASC
		LIMIT $1`, limit, a.aid, rescoreAll)
	if err != nil {
		return nil, fmt.Errorf("hunt: unscored open jobs: %w", err)
	}
	defer rows.Close()

	var result []Job
	for rows.Next() {
		j, scanErr := scanJobRow(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("hunt: unscored open jobs scan: %w", scanErr)
		}
		result = append(result, j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hunt: unscored open jobs rows: %w", err)
	}
	return result, nil
}

// UnscoredOpenJobsStats returns the count and oldest first_seen_at age of open
// jobs that have never been scored FOR THIS ACCOUNT. Same per-account
// semantics as UnscoredOpenJobs; used by the worker's periodic gauge
// refresher so the unscored-pool alert reflects the account's own backlog.
func (a *AccountStore) UnscoredOpenJobsStats(ctx context.Context) (UnscoredJobsStats, error) {
	var count int
	var oldest *time.Time
	err := a.s.pool.QueryRow(ctx, `
		SELECT COUNT(*), MIN(j.first_seen_at)
		FROM hunt_jobs j
		WHERE j.status = 'open'
		  AND NOT EXISTS (
		    SELECT 1 FROM account_job_scores s
		    WHERE s.account_id = $1 AND s.job_id = j.id
		  )`, a.aid).Scan(&count, &oldest)
	if err != nil {
		return UnscoredJobsStats{}, fmt.Errorf("hunt: unscored open jobs stats: %w", err)
	}
	stats := UnscoredJobsStats{Count: count}
	if oldest != nil {
		stats.OldestAge = time.Since(*oldest)
	}
	return stats, nil
}

// CountScored returns the number of open hunt_jobs rows THIS ACCOUNT has a
// score row for. Errors are silently swallowed (returns 0).
func (a *AccountStore) CountScored(ctx context.Context) int {
	var n int
	_ = a.s.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM hunt_jobs j
		WHERE j.status = 'open'
		  AND EXISTS (
		    SELECT 1 FROM account_job_scores s
		    WHERE s.account_id = $1 AND s.job_id = j.id
		  )`, a.aid,
	).Scan(&n)
	return n
}

// ── ratings (hunt_ratings, per-account) ─────────────────────────────────────

// Rate inserts or updates a hunt_ratings row FOR THIS ACCOUNT.
//
// Two-axis semantics (migration 012):
//   - triage: if non-empty, overwrites hunt_ratings.triage; if empty, keeps existing.
//   - stage:  if non-empty, overwrites hunt_ratings.stage;  if empty, keeps existing.
//   - note:   ALWAYS overwritten (even to ""), preserving the original note-contract.
//
// Single-axis callers: pass "" for the axis they do not own; the CASE guard
// in the ON CONFLICT clause preserves the existing DB value. The same CASE
// guard applies to note: passing note="" (which becomes NULL via nullStr)
// preserves the existing note, while a non-empty note overwrites it.
//
// Contrast with SetTriage and SetStage, which each preserve ALL other fields
// including note by not touching them at all. Do not unify these paths without
// understanding the divergence.
func (a *AccountStore) Rate(ctx context.Context, kind string, entryID int64, triage, stage, note string) error {
	_, err := a.s.pool.Exec(ctx, `
		INSERT INTO hunt_ratings (entry_kind, entry_id, account_id, triage, stage, note, rated_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW(), NOW())
		ON CONFLICT (entry_kind, entry_id, account_id) DO UPDATE
			SET triage     = CASE WHEN EXCLUDED.triage = '' THEN hunt_ratings.triage ELSE EXCLUDED.triage END,
			    stage      = CASE WHEN EXCLUDED.stage  = '' THEN hunt_ratings.stage  ELSE EXCLUDED.stage  END,
			    note       = CASE WHEN EXCLUDED.note IS NULL THEN hunt_ratings.note ELSE EXCLUDED.note END,
			    updated_at = NOW()`,
		kind, entryID, a.aid, triage, stage, nullStr(note),
	)
	if err != nil {
		return fmt.Errorf("hunt: rate: %w", err)
	}
	return nil
}

// RateExact unconditionally sets BOTH triage and stage (no CASE-preserve guards),
// plus overwrites note — FOR THIS ACCOUNT. Used when the caller owns the full
// two-axis state and must guarantee coherence — primarily the tracker tool,
// which enforces a single-observable-status contract (exactly one axis
// non-empty). Callers that want to touch only ONE axis while preserving the
// other should use SetTriage, SetStage, or Rate instead.
//
// NOTE: RateExact overwrites ALL THREE columns (triage, stage, note)
// unconditionally. Callers MUST own or reconstruct note before calling, or an
// empty note will wipe an existing one. trackerRate pre-fetches via GetRating
// in UpdateTrackedJob; a second caller must implement the same pre-fetch or
// pass the preserved note explicitly.
func (a *AccountStore) RateExact(ctx context.Context, kind string, entryID int64, triage, stage, note string) error {
	_, err := a.s.pool.Exec(ctx, `
		INSERT INTO hunt_ratings (entry_kind, entry_id, account_id, triage, stage, note, rated_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW(), NOW())
		ON CONFLICT (entry_kind, entry_id, account_id) DO UPDATE
			SET triage     = EXCLUDED.triage,
			    stage      = EXCLUDED.stage,
			    note       = EXCLUDED.note,
			    updated_at = NOW()`,
		kind, entryID, a.aid, triage, stage, nullStr(note),
	)
	if err != nil {
		return fmt.Errorf("hunt: rate exact: %w", err)
	}
	return nil
}

// SetTriage updates ONLY the triage column for a hunt_ratings row FOR THIS
// ACCOUNT, preserving the existing pipeline stage and note. Mirrors SetStage's
// note-preserve discipline but for the triage axis (migration 012).
//
// If no row exists, a new one is inserted with stage=” and note=NULL.
func (a *AccountStore) SetTriage(ctx context.Context, kind string, entryID int64, triage string) error {
	_, err := a.s.pool.Exec(ctx, `
		INSERT INTO hunt_ratings (entry_kind, entry_id, account_id, triage, rated_at, updated_at)
		VALUES ($1, $2, $3, $4, NOW(), NOW())
		ON CONFLICT (entry_kind, entry_id, account_id) DO UPDATE
			SET triage     = EXCLUDED.triage,
			    updated_at = NOW()`,
		kind, entryID, a.aid, triage,
	)
	if err != nil {
		return fmt.Errorf("hunt: set triage: %w", err)
	}
	return nil
}

// SetStage updates ONLY the stage column for a hunt_ratings row FOR THIS
// ACCOUNT, preserving the existing note. This is the correct write path for
// the inline stage dropdown in the jobs table, where the operator changes
// stage without touching the note field.
//
// If no row exists, a new one is inserted with an empty note (NULL).
func (a *AccountStore) SetStage(ctx context.Context, kind string, entryID int64, stage string) error {
	_, err := a.s.pool.Exec(ctx, `
		INSERT INTO hunt_ratings (entry_kind, entry_id, account_id, stage, rated_at, updated_at)
		VALUES ($1, $2, $3, $4, NOW(), NOW())
		ON CONFLICT (entry_kind, entry_id, account_id) DO UPDATE
			SET stage      = EXCLUDED.stage,
			    updated_at = NOW()`,
		kind, entryID, a.aid, stage,
	)
	if err != nil {
		return fmt.Errorf("hunt: set stage: %w", err)
	}
	return nil
}

// GetRating returns the rating for a specific (kind, entryID) pair FOR THIS
// ACCOUNT. Returns ErrNotFound if this account has no rating on the entry —
// a foreign account's rating is unreachable here.
func (a *AccountStore) GetRating(ctx context.Context, kind string, entryID int64) (*Rating, error) {
	var r Rating
	var note *string
	err := a.s.pool.QueryRow(ctx, `
		SELECT id, entry_kind, entry_id, COALESCE(user_name,''), triage, stage, note, rated_at, updated_at
		FROM hunt_ratings
		WHERE entry_kind = $1 AND entry_id = $2 AND account_id = $3`,
		kind, entryID, a.aid,
	).Scan(&r.ID, &r.EntryKind, &r.EntryID, &r.UserName, &r.Triage, &r.Stage, &note, &r.RatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("hunt: get rating: %w", err)
	}
	if note != nil {
		r.Note = *note
	}
	return &r, nil
}

// RatingFilter narrows ListRatings results. The account scope is bound by the
// facade — no User field exists by design.
type RatingFilter struct {
	Kind   string // entry_kind filter (e.g. KindBounty, KindJob)
	Stage  string // stage filter on either axis
	Limit  int    // default 50, max 500
	Offset int
}

// ListRatings returns THIS ACCOUNT's ratings newest-updated-first with
// optional filters.
func (a *AccountStore) ListRatings(ctx context.Context, f RatingFilter) ([]Rating, error) {
	limit := clampLimit(f.Limit, 50, 500)

	conds := []string{fmt.Sprintf("account_id = $%d", 1)}
	args := []any{a.aid}
	argN := 2

	if f.Kind != "" {
		conds = append(conds, fmt.Sprintf("entry_kind = $%d", argN))
		args = append(args, f.Kind)
		argN++
	}
	if f.Stage != "" {
		if f.Stage == StageSaved {
			conds = append(conds, fmt.Sprintf("triage = $%d", argN))
		} else {
			conds = append(conds, fmt.Sprintf("stage = $%d", argN))
		}
		args = append(args, f.Stage)
		argN++
	}

	where := "WHERE " + strings.Join(conds, " AND ")

	args = append(args, limit, f.Offset)
	q := fmt.Sprintf(`
		SELECT id, entry_kind, entry_id, COALESCE(user_name,''), triage, stage, note, rated_at, updated_at
		FROM hunt_ratings
		%s
		ORDER BY updated_at DESC
		LIMIT $%d OFFSET $%d`, where, argN, argN+1)

	rows, err := a.s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("hunt: list ratings: %w", err)
	}
	defer rows.Close()

	var result []Rating
	for rows.Next() {
		var r Rating
		var note *string
		if err := rows.Scan(
			&r.ID, &r.EntryKind, &r.EntryID, &r.UserName,
			&r.Triage, &r.Stage, &note, &r.RatedAt, &r.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("hunt: list ratings scan: %w", err)
		}
		if note != nil {
			r.Note = *note
		}
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hunt: list ratings rows: %w", err)
	}
	return result, nil
}

// TrackedJobRow is the postgres projection for the job_tracker MCP tool.
// Joins hunt_jobs with THIS ACCOUNT's hunt_ratings row.
// After migration 012 it carries BOTH axes:
//   - Triage: operator interest signal (” = untriaged)
//   - Stage:  pipeline position    (” = not in pipeline)
//
// The caller synthesizes a display status as: if Triage != "" → Triage, else Stage.
type TrackedJobRow struct {
	ID              int64
	Title           string
	Company         string
	URL             string
	Location        string
	SalaryMin       *int
	SalaryMax       *int
	SalaryCurrency  string
	SalaryInterval  string
	Triage          string // '' = untriaged
	Stage           string // '' = not in pipeline
	Note            string
	FirstSeenAt     time.Time
	RatingUpdatedAt time.Time
}

// TrackedFilter is the parameter bag for AccountStore.ListTrackedJobs.
//
// Stage vs Triage filtering (post-migration-012):
//   - Stage="saved" → filter by r.triage='saved' (triage axis)
//   - Stage=pipeline value → filter by r.stage=value (pipeline axis)
//   - Stage="" → all rated rows (both axes)
//
// The Stage field is kept as-is for backward compatibility with the job_tracker
// MCP tool, which uses the logical status name regardless of which DB column it maps to.
type TrackedFilter struct {
	Stage string // empty = all stages; "saved" routes to triage axis
	Limit int    // 0 = default 50, max 100
}

// ListTrackedJobs returns hunt_jobs rows that have a hunt_ratings row FOR THIS
// ACCOUNT, optionally filtered by stage (with axis routing for "saved").
// Returns rows and total count. Another account's ratings are unreachable —
// the JOIN carries the bound account predicate.
func (a *AccountStore) ListTrackedJobs(ctx context.Context, f TrackedFilter) ([]TrackedJobRow, int, error) {
	limit := f.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	var args []any
	filter := "r.account_id = $1"
	args = append(args, a.aid)
	if f.Stage != "" {
		// "saved" lives on the triage axis after migration 012.
		if f.Stage == StageSaved {
			filter += " AND r.triage = $2"
		} else {
			filter += " AND r.stage = $2"
		}
		args = append(args, f.Stage)
	}

	var total int
	if err := a.s.pool.QueryRow(ctx,
		"SELECT count(*) FROM hunt_jobs j JOIN hunt_ratings r ON r.entry_kind = 'job' AND r.entry_id = j.id WHERE "+filter,
		args...,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("hunt: count tracked jobs: %w", err)
	}

	n := len(args)
	rows, err := a.s.pool.Query(ctx, `
		SELECT j.id,
		       COALESCE(j.title,''), COALESCE(j.company,''), COALESCE(j.url,''),
		       COALESCE(j.location,''),
		       j.salary_min, j.salary_max,
		       COALESCE(j.salary_currency,''), COALESCE(j.salary_interval,''),
		       COALESCE(r.triage,''), COALESCE(r.stage,''), COALESCE(r.note,''),
		       j.first_seen_at, r.updated_at
		FROM hunt_jobs j JOIN hunt_ratings r ON r.entry_kind = 'job' AND r.entry_id = j.id
		WHERE `+filter+
		fmt.Sprintf(" ORDER BY r.updated_at DESC LIMIT $%d", n+1),
		append(args, limit)...,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("hunt: list tracked jobs: %w", err)
	}
	defer rows.Close()

	var result []TrackedJobRow
	for rows.Next() {
		var row TrackedJobRow
		var salMin, salMax *int
		if err := rows.Scan(
			&row.ID, &row.Title, &row.Company, &row.URL, &row.Location,
			&salMin, &salMax, &row.SalaryCurrency, &row.SalaryInterval,
			&row.Triage, &row.Stage, &row.Note, &row.FirstSeenAt, &row.RatingUpdatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("hunt: scan tracked job: %w", err)
		}
		row.SalaryMin = salMin
		row.SalaryMax = salMax
		result = append(result, row)
	}
	if result == nil {
		result = []TrackedJobRow{}
	}
	return result, total, rows.Err()
}

// CountShortlist returns the number of hunt_jobs rows with a hunt_ratings row
// FOR THIS ACCOUNT whose triage is in triageValues OR stage is in
// stageValues. Membership is ratings-driven (scores cannot change
// cardinality), so the account_job_scores join is not needed here.
// Errors are silently swallowed (returns 0).
func (a *AccountStore) CountShortlist(ctx context.Context, triageValues, stageValues []string) int {
	var n int
	_ = a.s.pool.QueryRow(ctx,
		`SELECT count(*) FROM hunt_jobs j JOIN hunt_ratings r
		 ON r.entry_kind = 'job' AND r.entry_id = j.id
		 WHERE r.account_id = $1 AND (r.triage = ANY($2::text[]) OR r.stage = ANY($3::text[]))`,
		a.aid, triageValues, stageValues,
	).Scan(&n)
	return n
}

// ToggleShortlistStar toggles a job's shortlist membership FOR THIS ACCOUNT.
//
// After migration 012 the star controls ONLY the triage axis; the pipeline stage
// is never touched by a star click.
//
// Toggle semantics:
//   - No row, or triage ∉ activeTriage                        → upsert triage=StageSaved → starred=true  (star on)
//   - triage ∈ softDemotable (interesting, saved)             → update  triage=”         → starred=false (star off)
//   - triage ∉ softDemotable but ∈ activeTriage (discarded)  → NO-OP                    → starred=false (deliberate negative decision)
//   - stage  ∈ advanced pipeline (applied/interview/offer)    → NO-OP                    → starred=true  (pipeline protection)
//
// Intentional asymmetry: the discarded NO-OP returns false (not a shortlist member)
// while the pipeline NO-OP returns true (in-pipeline jobs ARE shortlist members).
// A star click cannot silently undo a negative triage decision.
//
// Note preservation: note column is excluded from the ON CONFLICT SET list, so
// any existing note survives star toggling. This diverges intentionally from
// Rate, which DOES overwrite note. Do not merge these paths.
//
// activePipelineStages is the set of pipeline stages that protect against star-off
// (typically [claimed,applied,interview,offer]). softDemotable is the set of triage
// values a star-off is allowed to clear (StarSoftTriageValues).
func (a *AccountStore) ToggleShortlistStar(ctx context.Context, entryID int64, activePipelineStages, softDemotable []string) (bool, error) {
	tx, err := a.s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("hunt: toggle star: begin tx: %w", err)
	}
	// Unconditional deferred rollback: safe no-op after a successful Commit (pgx
	// returns ErrTxClosed without re-acquiring the connection). Guarantees the
	// connection is ALWAYS returned to the pool on every return path — including
	// early returns in the no-op branches and any unexpected panic.
	defer func() { _ = tx.Rollback(ctx) }()

	// Read current triage + stage — FOR UPDATE to lock the row.
	// pgx.ErrNoRows → no prior row; treat as star-on. Any other error → surface.
	var curTriage, curStage *string
	scanErr := tx.QueryRow(ctx,
		`SELECT triage, stage FROM hunt_ratings
		  WHERE entry_kind = 'job' AND entry_id = $1 AND account_id = $2
		  FOR UPDATE`,
		entryID, a.aid,
	).Scan(&curTriage, &curStage)
	if scanErr != nil && !errors.Is(scanErr, pgx.ErrNoRows) {
		return false, fmt.Errorf("hunt: toggle star id=%d: read triage: %w", entryID, scanErr)
	}

	// Pipeline protection: job at an advanced pipeline stage → no-op.
	// A star click can NEVER silently clear an applied/interview/offer position.
	if curStage != nil && stageIn(*curStage, activePipelineStages) {
		if err := tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("hunt: toggle star id=%d: commit no-op: %w", entryID, err)
		}
		return true, nil // starred unchanged — advanced pipeline stage
	}

	// Triage protection: a deliberate negative triage decision (e.g. discarded) is
	// never silently overwritten by a star click. Only softDemotable values
	// (interesting, saved) can be starred off; all other ∈ TriageStages values are
	// treated as protected and produce a no-op → return false (the star stays ☆).
	if curTriage != nil && *curTriage != "" && !stageIn(*curTriage, softDemotable) {
		if err := tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("hunt: toggle star id=%d: commit triage-protect: %w", entryID, err)
		}
		return false, nil // triage unchanged — deliberate negative decision
	}

	// Is the job already starred (triage ∈ softDemotable)?
	isStarred := curTriage != nil && *curTriage != "" && stageIn(*curTriage, softDemotable)

	var newTriage string // '' = untriaged (star off)
	if !isStarred {
		newTriage = StageSaved // star on
	}

	// Upsert triage; note and stage are NOT touched.
	// On INSERT (no prior row) stage defaults to '' and note to NULL.
	if _, err := tx.Exec(ctx, `
		INSERT INTO hunt_ratings (entry_kind, entry_id, account_id, triage, rated_at, updated_at)
		VALUES ('job', $1, $2, $3, NOW(), NOW())
		ON CONFLICT (entry_kind, entry_id, account_id) DO UPDATE
			SET triage     = EXCLUDED.triage,
			    updated_at = NOW()`,
		entryID, a.aid, newTriage,
	); err != nil {
		return false, fmt.Errorf("hunt: toggle star id=%d: upsert: %w", entryID, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("hunt: toggle star id=%d: commit: %w", entryID, err)
	}
	return !isStarred, nil
}

// ── per-account hunt settings (account_hunt_settings) ───────────────────────

// GetHuntSettings loads THIS ACCOUNT's account_hunt_settings row. Returns
// (zero-value, false, nil) when the account has no row — a missing row means
// DISABLED; env fallbacks layer on top (huntworker merge), never resurrect an
// absent row into an enabled account.
func (a *AccountStore) GetHuntSettings(ctx context.Context) (accounts.AccountHuntSettings, bool, error) {
	return accounts.GetAccountHuntSettings(ctx, a.s.pool, a.aid)
}

// SaveHuntSettings upserts THIS ACCOUNT's account_hunt_settings row. The
// write is keyed by the bound account id — a foreign row is unreachable
// through this facade by construction.
func (a *AccountStore) SaveHuntSettings(ctx context.Context, s accounts.AccountHuntSettings) error {
	return accounts.SaveAccountHuntSettings(ctx, a.s.pool, a.aid, s)
}

// ── shortlist ───────────────────────────────────────────────────────────────

// accountShortlistJoin renders the FROM + JOIN block for ListShortlist. The
// score join is LEFT: a job the account never scored still shortlists (as
// unscored) — ratings (hunt_ratings rows for this account) drive membership,
// not scores. aidIdx is the bind position of the account id (shared by both
// joins — one bind covers ratings and scores since they key on the same aid).
func accountShortlistJoin(aidIdx int) string {
	return fmt.Sprintf(`FROM hunt_jobs j
		JOIN hunt_ratings r ON r.entry_kind = 'job' AND r.entry_id = j.id AND r.account_id = $%d
		LEFT JOIN account_job_scores s ON s.job_id = j.id AND s.account_id = $%d`, aidIdx, aidIdx)
}

// shortlistDefaultOrder is the fallback ORDER BY when ShortlistQuery.OrderBy is empty.
const shortlistDefaultOrder = "s.fit_score DESC NULLS LAST, j.company"

// safeOrderByPatterns is the allowlist of column expressions that may appear
// in an ORDER BY clause. BH-5: defense-in-depth against SQL injection — even
// though admintable.Spec.OrderBy is author-declared, ListShortlist is a public
// API. Each entry is a full "column [ASC|DESC] [NULLS LAST]" expression.
// Score columns are s.* (account_job_scores); corpus columns stay j.*.
var safeOrderByPatterns = map[string]bool{
	"s.fit_score DESC NULLS LAST": true,
	"s.fit_score ASC":             true,
	"j.company ASC":               true,
	"j.company DESC":              true,
	"j.title ASC":                 true,
	"j.title DESC":                true,
	"j.posted_at DESC":            true,
	"j.posted_at ASC":             true,
	"s.scored_at DESC":            true,
	"s.scored_at ASC":             true,
	"j.location ASC":              true,
	"j.location DESC":             true,
	"j.salary_max DESC":           true,
	"j.salary_max ASC":            true,
	"r.updated_at DESC":           true,
	"r.updated_at ASC":            true,
	"r.triage ASC":                true,
	"r.triage DESC":               true,
	"r.stage ASC":                 true,
	"r.stage DESC":                true,
	shortlistDefaultOrder:         true,
}

// isSafeOrderBy reports whether orderBy is in the allowlist of safe ORDER BY
// expressions. BH-5: prevents SQL injection via raw OrderBy interpolation.
func isSafeOrderBy(orderBy string) bool {
	return safeOrderByPatterns[strings.TrimSpace(orderBy)]
}

// ListShortlist returns hunt_jobs rows that have a hunt_ratings row FOR THIS
// ACCOUNT whose triage is in q.TriageValues OR stage is in q.StageValues,
// applying optional FilterSpec conditions and pagination. Returns the matching
// rows and the pre-pagination total count.
//
// Score columns (fit_score, fit_band, success_band, over_under, scored_at)
// come from account_job_scores FOR THIS ACCOUNT via the LEFT JOIN, and the
// ratings join is keyed by the same bound account — a foreign account's
// membership, rating, or score is structurally unreachable here.
//
// The security-critical account isolation guard lives here (not in the
// caller), so that both the live lister and the isolation test exercise the
// same code.
func (a *AccountStore) ListShortlist(ctx context.Context, q ShortlistQuery) ([]ShortlistRow, int, error) {
	// Build the full WHERE clause.
	// q.WhereConds ($1…$N) from FilterSpec precedes the axis guards at $N+1/$N+2;
	// the account bind for BOTH joins sits at $N+3.
	filter := "TRUE"
	if strings.TrimSpace(q.WhereConds) != "" {
		filter = q.WhereConds
	}
	n := len(q.WhereArgs)
	//nolint:gosec // filter = q.WhereConds (author-controlled FilterSpec SQLExpr/SQLExprs + literal operators); all URL values are bind args; isolation guards are literal templates.
	fullWhere := fmt.Sprintf(
		"%s AND (r.triage = ANY($%d::text[]) OR r.stage = ANY($%d::text[]))",
		filter, n+1, n+2,
	)
	join := accountShortlistJoin(n + 3)
	baseArgs := append(append([]any{}, q.WhereArgs...), q.TriageValues, q.StageValues, a.aid)

	// COUNT — total matching rows before pagination. Neither join can change
	// cardinality (UNIQUE(entry_kind,entry_id,account_id) → at most one r row;
	// UNIQUE(account_id,job_id) → at most one s row), so counting through the
	// same join keeps the two queries in lockstep.
	var total int
	if err := a.s.pool.QueryRow(ctx,
		"SELECT count(*) "+join+" WHERE "+fullWhere,
		baseArgs...,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("hunt: count shortlist: %w", err)
	}

	orderBy := shortlistDefaultOrder
	if q.OrderBy != "" {
		// BH-5: validate OrderBy against an allowlist of safe column expressions.
		// admintable.Spec.OrderBy is author-declared, but ListShortlist is a public
		// API callable from non-admin paths (MCP tools). Defense-in-depth: reject
		// anything not in the allowlist rather than interpolating raw input.
		if !isSafeOrderBy(q.OrderBy) {
			slog.Warn("hunt: rejecting unsafe OrderBy, using default", slog.String("orderby", q.OrderBy))
			orderBy = shortlistDefaultOrder
		} else {
			orderBy = q.OrderBy
		}
	}

	const selectCols = `SELECT j.id,
		       COALESCE(j.title,''), COALESCE(j.company,''), COALESCE(j.url,''),
		       COALESCE(j.location,''),
		       s.fit_score, COALESCE(s.fit_band,''),
		       COALESCE(s.success_band,''), COALESCE(s.over_under,''),
		       j.salary_min, j.salary_max,
		       COALESCE(j.salary_currency,''), COALESCE(j.salary_interval,''),
		       j.posted_at, s.scored_at,
		       COALESCE(r.triage,''), COALESCE(r.stage,''), COALESCE(r.note,''), r.rated_at `

	var query string
	queryArgs := append([]any{}, baseArgs...)
	if q.Limit > 0 {
		//nolint:gosec // orderBy from admintable.Spec.OrderBy (author-declared SQLExpr + ASC/DESC/NULLS LAST); pagination clause = literal template; no URL input.
		query = selectCols + join + " WHERE " + fullWhere +
			fmt.Sprintf(" ORDER BY %s LIMIT $%d OFFSET $%d", orderBy, n+4, n+5)
		queryArgs = append(queryArgs, q.Limit, q.Offset)
	} else {
		//nolint:gosec
		query = selectCols + join + " WHERE " + fullWhere + " ORDER BY " + orderBy
	}

	rows, err := a.s.pool.Query(ctx, query, queryArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("hunt: list shortlist: %w", err)
	}
	defer rows.Close()

	var out []ShortlistRow
	for rows.Next() {
		var row ShortlistRow
		if err := rows.Scan(
			&row.ID, &row.Title, &row.Company, &row.URL,
			&row.Location,
			&row.FitScore, &row.FitBand,
			&row.SuccessBand, &row.OverUnder,
			&row.SalaryMin, &row.SalaryMax,
			&row.SalaryCurrency, &row.SalaryInterval,
			&row.PostedAt, &row.ScoredAt,
			&row.Triage, &row.Stage, &row.Note, &row.RatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("hunt: scan shortlist row: %w", err)
		}
		out = append(out, row)
	}
	return out, total, rows.Err()
}

// ── bounties (shared corpus read; ratings join is account-scoped) ───────────

// ListBounties returns bounties newest-first with optional filters FOR THIS
// ACCOUNT's view — the corpus rows themselves are global, but the optional
// Stage filter joins THIS ACCOUNT's hunt_ratings row only: account A's bounty
// rating can never float a row into account B's stage-filtered list.
// By default, only status='open' rows are returned. Set IncludeClosed=true for all.
// After fetching, triggers a lazy background GitHub enrichment pass for open rows
// that haven't been checked within defaultEnrichTTL (non-blocking).
func (a *AccountStore) ListBounties(ctx context.Context, f BountyFilter) ([]Bounty, error) {
	limit := clampLimit(f.Limit, 50, 500)

	conds := []string{}
	args := []any{}
	argN := 1

	if !f.IncludeClosed {
		conds = append(conds, "status = 'open'")
	}
	if f.Source != "" {
		conds = append(conds, fmt.Sprintf("source = $%d", argN))
		args = append(args, f.Source)
		argN++
	}
	if f.MinAmount > 0 {
		conds = append(conds, fmt.Sprintf("amount_cents >= $%d", argN))
		args = append(args, f.MinAmount)
		argN++
	}
	if len(f.Skills) > 0 {
		conds = append(conds, fmt.Sprintf("skills && $%d::text[]", argN))
		args = append(args, f.Skills)
		argN++
	}

	// Stage filter via LEFT JOIN hunt_ratings — scoped to the bound account
	// (uuid.Nil → the join matches nothing → stage-filtered results are empty,
	// fail-closed). "saved" lives on the triage axis after migration 012;
	// other stages are on the stage column. The JOIN is only added when
	// f.Stage is set.
	joinClause := ""
	if f.Stage != "" {
		joinClause = fmt.Sprintf(
			"LEFT JOIN hunt_ratings r ON r.entry_kind = 'bounty' AND r.entry_id = hunt_bounties.id AND r.account_id = $%d", argN)
		args = append(args, a.aid)
		argN++
		if f.Stage == StageSaved {
			conds = append(conds, fmt.Sprintf("r.triage = $%d", argN))
		} else {
			conds = append(conds, fmt.Sprintf("r.stage = $%d", argN))
		}
		args = append(args, f.Stage)
		argN++
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	args = append(args, limit, f.Offset)
	q := fmt.Sprintf(`
		SELECT id, dedup_hash, title, url, org, source, amount_cents, currency,
		       issue_number, skills, description, relevance, posted_at,
		       first_seen_at, last_seen_at, status, closed_at, last_checked_at
		FROM hunt_bounties
		%s
		%s
		ORDER BY last_seen_at DESC
		LIMIT $%d OFFSET $%d`, joinClause, where, argN, argN+1)

	rows, err := a.s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("hunt: list bounties: %w", err)
	}
	defer rows.Close()

	var result []Bounty
	for rows.Next() {
		b, scanErr := scanBountyRow(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("hunt: list bounties scan: %w", scanErr)
		}
		result = append(result, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hunt: list bounties rows: %w", err)
	}

	// Lazy enrichment: background GitHub status check for open rows. Non-blocking.
	// Uses a detached context (not coupled to the request) with a 30s hard deadline
	// to ensure graceful shutdown even if the caller's ctx is cancelled.
	// G118: context.Background() is intentional — enrich must outlive the request context.
	//
	// #184 fix: bounded by enrichSem (cap=5) to prevent goroutine pile-up under
	// rapid ListBounties calls. Non-blocking acquire — if the semaphore is full,
	// enrichment is skipped (best-effort: the next ListBounties call will retry).
	s := a.s
	if s.enricher != nil && len(result) > 0 {
		select {
		case s.enrichSem <- struct{}{}:
			snap := make([]Bounty, len(result))
			copy(snap, result)
			go func() { //nolint:gosec // G118: intentional detached context with explicit 30s deadline
				defer func() { <-s.enrichSem }()
				enrichCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				s.enricher.EnrichBountyStatus(enrichCtx, s, snap, defaultEnrichTTL)
			}()
		default:
			// Semaphore full — skip enrichment this cycle. Non-fatal: next call retries.
			// OBS-6: bump the skip counter so operators can tune enrichMaxConcurrent.
			if onEnrichSkipped != nil {
				onEnrichSkipped()
			}
		}
	}

	return result, nil
}
