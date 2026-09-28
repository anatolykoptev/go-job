package hunt

// account_store.go — the account-scoped data-plane facade (plan ADR-15).
//
// Per-account judgment data (account_job_scores today; ratings/settings move
// in P3) is reachable ONLY through this type: the unscoped statement shape is
// deliberately absent from *Store so "forgot the WHERE account_id" is not an
// expressible bug — the account id is bound once at ForAccount and every
// query below carries its predicate. Omission is compile-loud: a caller
// without an account cannot even construct the call.
//
// The shared hunt_* corpus (hunt_jobs rows themselves, bounties, freelance,
// security, settings) stays on *Store — it is global by design (ADR-6).

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AccountStore is the account-scoped view of the hunt store. Obtain it via
// (*Store).ForAccount — never construct it directly.
type AccountStore struct {
	pool *pgxpool.Pool
	aid  uuid.UUID
}

// ForAccount binds a per-account facade for account-scoped reads and writes.
// The caller must have resolved aid from the request's verified identity
// (accounts.AccountFrom / the pinned operator under AUTH_DRIVER=hmac);
// uuid.Nil is accepted but fails loud at write time (account_id FK) and reads
// match nothing — there is no "all accounts" fast path through this type.
func (s *Store) ForAccount(aid uuid.UUID) *AccountStore {
	return &AccountStore{pool: s.pool, aid: aid}
}

// AccountID returns the account this facade is bound to.
func (a *AccountStore) AccountID() uuid.UUID { return a.aid }

// ShortlistRow is the postgres projection for the /admin/shortlist page.
// It joins hunt_jobs with hunt_ratings for a given user, filtered to the
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
	User string
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
// the global columns are dead to writes from P2 on.
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
	ct, err := a.pool.Exec(ctx, `
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
// The "unscored" predicate is per-account now: a job scored by account A is
// still unscored for B — each account's sweep drains its own pool. This
// replaces the hunt_jobs.scored_at IS NULL probe (the scored_at column is
// dead for account views; it moves to account_job_scores.scored_at).
func (a *AccountStore) UnscoredOpenJobs(ctx context.Context, limit int, rescoreAll bool) ([]Job, error) {
	limit = clampLimit(limit, 50, 500)
	rows, err := a.pool.Query(ctx, `
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
	err := a.pool.QueryRow(ctx, `
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
	_ = a.pool.QueryRow(ctx, `
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

// accountShortlistJoin renders the FROM + JOIN block for ListShortlist. The
// score join is LEFT: a job the account never scored still shortlists (as
// unscored) — ratings (rows in hunt_ratings, P3 scope) drive membership, not
// scores. aidIdx is the bind position of the account id.
func accountShortlistJoin(aidIdx int) string {
	return fmt.Sprintf(`FROM hunt_jobs j
		JOIN hunt_ratings r ON r.entry_kind = 'job' AND r.entry_id = j.id
		LEFT JOIN account_job_scores s ON s.job_id = j.id AND s.account_id = $%d`, aidIdx)
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

// ListShortlist returns hunt_jobs rows that have a hunt_ratings row for the given
// user (q.User) whose triage is in q.TriageValues OR stage is in q.StageValues,
// applying optional FilterSpec conditions and pagination. Returns the matching
// rows and the pre-pagination total count.
//
// Score columns (fit_score, fit_band, success_band, over_under, scored_at)
// come from account_job_scores FOR THIS ACCOUNT via the LEFT JOIN — a foreign
// account's score is structurally unreachable here. The r.user_name guard is
// unchanged (ratings scoping is P3 — hunt_ratings still keys on user_name).
//
// The security-critical user_name and axis isolation guards live here (not in the
// caller), so that both the live lister and the isolation test exercise the same code.
func (a *AccountStore) ListShortlist(ctx context.Context, q ShortlistQuery) ([]ShortlistRow, int, error) {
	// Build the full WHERE clause.
	// q.WhereConds ($1…$N) from FilterSpec precedes the isolation guards at $N+1/$N+2/$N+3;
	// the account bind for the score join sits at $N+4.
	filter := "TRUE"
	if strings.TrimSpace(q.WhereConds) != "" {
		filter = q.WhereConds
	}
	n := len(q.WhereArgs)
	//nolint:gosec // filter = q.WhereConds (author-controlled FilterSpec SQLExpr/SQLExprs + literal operators); all URL values are bind args; isolation guards are literal templates.
	fullWhere := fmt.Sprintf(
		"%s AND r.user_name = $%d AND (r.triage = ANY($%d::text[]) OR r.stage = ANY($%d::text[]))",
		filter, n+1, n+2, n+3,
	)
	join := accountShortlistJoin(n + 4)
	baseArgs := append(append([]any{}, q.WhereArgs...), q.User, q.TriageValues, q.StageValues, a.aid)

	// COUNT — total matching rows before pagination. The LEFT score join cannot
	// change cardinality (UNIQUE(account_id, job_id) → at most one s row), so
	// counting through the same join keeps the two queries in lockstep.
	var total int
	if err := a.pool.QueryRow(ctx,
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
			fmt.Sprintf(" ORDER BY %s LIMIT $%d OFFSET $%d", orderBy, n+5, n+6)
		queryArgs = append(queryArgs, q.Limit, q.Offset)
	} else {
		//nolint:gosec
		query = selectCols + join + " WHERE " + fullWhere + " ORDER BY " + orderBy
	}

	rows, err := a.pool.Query(ctx, query, queryArgs...)
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
