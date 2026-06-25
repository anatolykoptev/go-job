package hunt

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema/*.sql
var schemaFS embed.FS

// ErrNotFound is returned when a requested entry does not exist.
var ErrNotFound = errors.New("hunt: entry not found")

// Outcome describes the result of an Upsert operation.
type Outcome int

const (
	// OutcomeCreated means a new row was inserted.
	OutcomeCreated Outcome = iota
	// OutcomeMerged means an existing row was touched (last_seen_at updated).
	OutcomeMerged
	// OutcomeSkipped means the record was intentionally skipped (e.g. empty URL filtered by caller).
	OutcomeSkipped
	// OutcomeError means a DB or marshal failure occurred. Distinct from OutcomeSkipped so
	// gojob_hunt_ingest_total{outcome="error"} is visible separately in dashboards.
	OutcomeError
)

// String returns the Prometheus-safe label value for the outcome.
func (o Outcome) String() string {
	switch o {
	case OutcomeCreated:
		return "created"
	case OutcomeMerged:
		return "merged"
	case OutcomeSkipped:
		return "skipped"
	default:
		return "error"
	}
}

// defaultEnrichTTL is how long a bounty can go unchecked before lazy enrichment re-fetches it.
const defaultEnrichTTL = 1 * time.Hour

// BountyEnricher triggers background GitHub status enrichment for open bounties.
// The concrete implementation lives in internal/hunt/enrich to avoid import cycles.
type BountyEnricher interface {
	EnrichBountyStatus(ctx context.Context, store StatusUpdater, entries []Bounty, maxAge time.Duration)
}

// StatusUpdater is the subset of Store that the enricher needs (avoids circular import).
type StatusUpdater interface {
	UpdateStatus(ctx context.Context, kind string, id int64, status string, closedAt *time.Time) error
}

// Notifier fires on ingest outcomes.
// NotifyNewJob accepts a *ScoreResult so the card renderer can produce a
// full fit-card (score != nil) or a degraded recency-only card (score == nil).
// Callers pass nil when scoring is disabled or the score is not yet available.
type Notifier interface {
	NotifyNewBounty(b Bounty)
	NotifyNewJob(j Job, score *ScoreResult)
	NotifyNewFreelance(f Freelance)
	NotifyNewSecurity(s Security)
}

// StatusUpdate carries one status change for UpdateStatusBatch.
type StatusUpdate struct {
	ID       int64
	Status   string
	ClosedAt *time.Time
}

// Store is the Postgres-backed hunt store.
type Store struct {
	pool     *pgxpool.Pool
	enricher BountyEnricher
	notifier Notifier
}

// NewStore returns a Store using the given pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Pool returns the underlying pgxpool.Pool used by this Store.
// Callers that need direct pool access (e.g. score.LoadProfile) should use this
// accessor rather than coupling to the internal field.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// SetEnricher wires a background GitHub enricher that triggers on ListBounties reads.
func (s *Store) SetEnricher(e BountyEnricher) { s.enricher = e }

// SetNotifier wires a Telegram notifier that fires on OutcomeCreated ingest events.
func (s *Store) SetNotifier(n Notifier) { s.notifier = n }

// NotifyJobIfOpen fires NotifyNewJob on the wired notifier for an open job.
// It is a no-op if the notifier is nil or the job is not open/empty-status.
// Called by the MCP path (persistJobListings) when HUNT_NOTIFY_ON_SEARCH=true.
// score is nil because the MCP path scores lazily (Decision 5) — the worker's
// next cycle picks up unscored rows via the scored_at IS NULL sweep.
func (s *Store) NotifyJobIfOpen(j Job) {
	if s.notifier != nil && (j.Status == StatusOpen || j.Status == "") {
		s.notifier.NotifyNewJob(j, nil)
	}
}

// Migrate runs schema migrations in lexical order. Idempotent (all DDL uses IF NOT EXISTS).
func (s *Store) Migrate(ctx context.Context) error {
	entries, err := schemaFS.ReadDir("schema")
	if err != nil {
		return fmt.Errorf("hunt: read schema dir: %w", err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("hunt: acquire migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SET search_path TO public"); err != nil {
		return fmt.Errorf("hunt: set search_path: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		data, err := schemaFS.ReadFile("schema/" + entry.Name())
		if err != nil {
			return fmt.Errorf("hunt: read %s: %w", entry.Name(), err)
		}
		if _, err := conn.Exec(ctx, string(data)); err != nil {
			return fmt.Errorf("hunt: execute %s: %w", entry.Name(), err)
		}
	}
	return nil
}

// UpsertBounty inserts a new bounty or updates last_seen_at on dedup_hash conflict.
// Returns (id, OutcomeCreated) for new rows, (id, OutcomeMerged) for existing rows.
// Uses the xmax=0 Postgres trick: xmax is 0 iff the row was just inserted.
// Status is preserved on conflict: once closed/merged, a re-ingest with "open" does NOT revert it.
func (s *Store) UpsertBounty(ctx context.Context, b Bounty) (id int64, outcome Outcome, err error) {
	status := b.Status
	if status == "" {
		status = StatusOpen
	}
	var created bool
	err = s.pool.QueryRow(ctx, `
		INSERT INTO hunt_bounties
			(dedup_hash, title, url, org, source, amount_cents, currency, issue_number,
			 skills, description, relevance, posted_at, raw, status, closed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		ON CONFLICT (dedup_hash) DO UPDATE
			SET last_seen_at = NOW(),
			    -- closed/merged is a terminal state for ingest; only enricher (UpdateStatus) can promote between non-open states
			    status = CASE WHEN hunt_bounties.status = 'open' THEN EXCLUDED.status ELSE hunt_bounties.status END,
			    closed_at = COALESCE(hunt_bounties.closed_at, EXCLUDED.closed_at)
		RETURNING id, (xmax = 0) AS created`,
		b.DedupHash, b.Title, b.URL, nullStr(b.Org), b.Source,
		nullInt64(b.AmountCents), nullStr(b.Currency), nullInt(b.IssueNumber),
		nullSlice(b.Skills), nullStr(b.Description), nullFloat32(b.Relevance),
		b.PostedAt, nullRaw(b.Raw), status, b.ClosedAt,
	).Scan(&id, &created)
	if err != nil {
		return 0, OutcomeError, fmt.Errorf("hunt: upsert bounty: %w", err)
	}
	if created {
		// Only notify for open bounties — prevents Telegram blast on first deploy when
		// historical claimed/completed/archived bounties are ingested for the first time.
		if s.notifier != nil && status == StatusOpen {
			s.notifier.NotifyNewBounty(b)
		}
		return id, OutcomeCreated, nil
	}
	return id, OutcomeMerged, nil
}

// BountyFilter narrows ListBounties results.
type BountyFilter struct {
	Source        string
	MinAmount     int64
	Skills        []string
	Stage         string // join hunt_ratings
	IncludeClosed bool   // when false (default), only status='open' rows are returned
	Limit         int    // default 50, max 500
	Offset        int
}

// ListBounties returns bounties newest-first with optional filters.
// By default, only status='open' rows are returned. Set IncludeClosed=true for all.
// After fetching, triggers a lazy background GitHub enrichment pass for open rows
// that haven't been checked within defaultEnrichTTL (non-blocking).
func (s *Store) ListBounties(ctx context.Context, f BountyFilter) ([]Bounty, error) {
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
	// TODO(phase2): wire f.Stage via hunt_ratings join

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
		ORDER BY last_seen_at DESC
		LIMIT $%d OFFSET $%d`, where, argN, argN+1)

	rows, err := s.pool.Query(ctx, q, args...)
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
	if s.enricher != nil && len(result) > 0 {
		snap := make([]Bounty, len(result))
		copy(snap, result)
		go func() { //nolint:gosec // G118: intentional detached context with explicit 30s deadline
			enrichCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			s.enricher.EnrichBountyStatus(enrichCtx, s, snap, defaultEnrichTTL)
		}()
	}

	return result, nil
}

// GetBounty returns a single bounty by id; ErrNotFound if missing.
func (s *Store) GetBounty(ctx context.Context, id int64) (*Bounty, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, dedup_hash, title, url, org, source, amount_cents, currency,
		       issue_number, skills, description, relevance, posted_at,
		       first_seen_at, last_seen_at, status, closed_at, last_checked_at
		FROM hunt_bounties WHERE id = $1`, id)
	b, err := scanBountyRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("hunt: get bounty: %w", err)
	}
	return &b, nil
}

// UpsertJob inserts a new job or updates last_seen_at on dedup_hash conflict.
// Status is preserved on conflict: once closed, a re-ingest with "open" does NOT revert it.
func (s *Store) UpsertJob(ctx context.Context, j Job) (id int64, outcome Outcome, err error) {
	status := j.Status
	if status == "" {
		status = StatusOpen
	}
	var created bool
	err = s.pool.QueryRow(ctx, `
		INSERT INTO hunt_jobs
			(dedup_hash, title, company, url, source, external_id, location, remote,
			 job_type, experience, salary_min, salary_max, salary_currency, salary_interval,
			 skills, tags, description, posted_at, raw, status, closed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
		ON CONFLICT (dedup_hash) DO UPDATE
			SET last_seen_at = NOW(),
			    -- closed/merged is a terminal state for ingest; only enricher (UpdateStatus) can promote between non-open states
			    status = CASE WHEN hunt_jobs.status = 'open' THEN EXCLUDED.status ELSE hunt_jobs.status END,
			    closed_at = COALESCE(hunt_jobs.closed_at, EXCLUDED.closed_at)
		RETURNING id, (xmax = 0) AS created`,
		j.DedupHash, j.Title, nullStr(j.Company), j.URL, j.Source,
		nullStr(j.ExternalID), nullStr(j.Location), nullStr(j.Remote),
		nullStr(j.JobType), nullStr(j.Experience),
		nullInt(j.SalaryMin), nullInt(j.SalaryMax),
		nullStr(j.SalaryCurrency), nullStr(j.SalaryInterval),
		nullSlice(j.Skills), nullSlice(j.Tags), nullStr(j.Description),
		j.PostedAt, nullRaw(j.Raw), status, j.ClosedAt,
	).Scan(&id, &created)
	if err != nil {
		return 0, OutcomeError, fmt.Errorf("hunt: upsert job: %w", err)
	}
	if created {
		return id, OutcomeCreated, nil
	}
	return id, OutcomeMerged, nil
}

// SetJobScore persists the fit-scoring result for a job.
// It updates ONLY the score columns (fit_score, fit_band, success_band,
// over_under, score_rationale, scored_at). It does NOT touch status,
// closed_at, first_seen_at, or any ingest fields — scoring is orthogonal
// to ingest (see UpsertJob invariant at store.go:299-301).
func (s *Store) SetJobScore(ctx context.Context, id int64, sr ScoreResult) error {
	rationale := scoreRationale{
		FitReasons:       sr.FitReasons,
		FitGaps:          sr.FitGaps,
		SuccessReasoning: sr.SuccessReasoning,
	}
	rationaleJSON, err := json.Marshal(rationale)
	if err != nil {
		return fmt.Errorf("hunt: marshal score rationale: %w", err)
	}
	ct, err := s.pool.Exec(ctx, `
		UPDATE hunt_jobs
		SET fit_score       = $1,
		    fit_band        = $2,
		    success_band    = $3,
		    over_under      = $4,
		    score_rationale = $5,
		    scored_at       = $6
		WHERE id = $7`,
		sr.FitScore, sr.FitBand, sr.SuccessBand, sr.OverUnder,
		rationaleJSON, sr.ScoredAt, id,
	)
	if err != nil {
		return fmt.Errorf("hunt: set job score: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("hunt: set job score: %w", ErrNotFound)
	}
	return nil
}

// JobFilter narrows ListJobs results.
type JobFilter struct {
	Source        string
	Company       string
	Remote        string
	IncludeClosed bool // when false (default), only status='open' rows are returned
	Limit         int
	Offset        int
}

// ListJobs returns jobs newest-first with optional filters.
// By default, only status='open' rows are returned. Set IncludeClosed=true for all.
func (s *Store) ListJobs(ctx context.Context, f JobFilter) ([]Job, error) {
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
	if f.Company != "" {
		conds = append(conds, fmt.Sprintf("company ILIKE $%d", argN))
		args = append(args, "%"+f.Company+"%")
		argN++
	}
	if f.Remote != "" {
		conds = append(conds, fmt.Sprintf("remote = $%d", argN))
		args = append(args, f.Remote)
		argN++
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	args = append(args, limit, f.Offset)
	q := fmt.Sprintf(`
		SELECT id, dedup_hash, title, company, url, source, external_id, location, remote,
		       job_type, experience, salary_min, salary_max, salary_currency, salary_interval,
		       skills, tags, description, posted_at, first_seen_at, last_seen_at,
		       status, closed_at, last_checked_at
		FROM hunt_jobs
		%s
		ORDER BY last_seen_at DESC
		LIMIT $%d OFFSET $%d`, where, argN, argN+1)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("hunt: list jobs: %w", err)
	}
	defer rows.Close()

	var result []Job
	for rows.Next() {
		var j Job
		var company, extID, location, remote, jobType, exp, cur, interval, desc *string
		var salMin, salMax *int
		if err := rows.Scan(
			&j.ID, &j.DedupHash, &j.Title, &company, &j.URL, &j.Source,
			&extID, &location, &remote, &jobType, &exp,
			&salMin, &salMax, &cur, &interval,
			&j.Skills, &j.Tags, &desc, &j.PostedAt,
			&j.FirstSeenAt, &j.LastSeenAt,
			&j.Status, &j.ClosedAt, &j.LastCheckedAt,
		); err != nil {
			return nil, fmt.Errorf("hunt: list jobs scan: %w", err)
		}
		if company != nil {
			j.Company = *company
		}
		if extID != nil {
			j.ExternalID = *extID
		}
		if location != nil {
			j.Location = *location
		}
		if remote != nil {
			j.Remote = *remote
		}
		if jobType != nil {
			j.JobType = *jobType
		}
		if exp != nil {
			j.Experience = *exp
		}
		if salMin != nil {
			j.SalaryMin = *salMin
		}
		if salMax != nil {
			j.SalaryMax = *salMax
		}
		if cur != nil {
			j.SalaryCurrency = *cur
		}
		if interval != nil {
			j.SalaryInterval = *interval
		}
		if desc != nil {
			j.Description = *desc
		}
		result = append(result, j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hunt: list jobs rows: %w", err)
	}
	return result, nil
}

// GetJob returns a single job by id; ErrNotFound if missing.
func (s *Store) GetJob(ctx context.Context, id int64) (*Job, error) {
	var j Job
	var company, extID, location, remote, jobType, exp, cur, interval, desc *string
	var salMin, salMax *int
	err := s.pool.QueryRow(ctx, `
		SELECT id, dedup_hash, title, company, url, source, external_id, location, remote,
		       job_type, experience, salary_min, salary_max, salary_currency, salary_interval,
		       skills, tags, description, posted_at, first_seen_at, last_seen_at,
		       status, closed_at, last_checked_at
		FROM hunt_jobs WHERE id = $1`, id).Scan(
		&j.ID, &j.DedupHash, &j.Title, &company, &j.URL, &j.Source,
		&extID, &location, &remote, &jobType, &exp,
		&salMin, &salMax, &cur, &interval,
		&j.Skills, &j.Tags, &desc, &j.PostedAt,
		&j.FirstSeenAt, &j.LastSeenAt,
		&j.Status, &j.ClosedAt, &j.LastCheckedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("hunt: get job: %w", err)
	}
	if company != nil {
		j.Company = *company
	}
	if extID != nil {
		j.ExternalID = *extID
	}
	if location != nil {
		j.Location = *location
	}
	if remote != nil {
		j.Remote = *remote
	}
	if jobType != nil {
		j.JobType = *jobType
	}
	if exp != nil {
		j.Experience = *exp
	}
	if salMin != nil {
		j.SalaryMin = *salMin
	}
	if salMax != nil {
		j.SalaryMax = *salMax
	}
	if cur != nil {
		j.SalaryCurrency = *cur
	}
	if interval != nil {
		j.SalaryInterval = *interval
	}
	if desc != nil {
		j.Description = *desc
	}
	return &j, nil
}

// UpsertFreelance inserts a new freelance project or updates last_seen_at on conflict.
// Status is preserved on conflict: once archived/closed, a re-ingest with "open" does NOT revert it.
func (s *Store) UpsertFreelance(ctx context.Context, f Freelance) (id int64, outcome Outcome, err error) {
	status := f.Status
	if status == "" {
		status = StatusOpen
	}
	var created bool
	err = s.pool.QueryRow(ctx, `
		INSERT INTO hunt_freelance
			(dedup_hash, title, url, platform, source, budget_min, budget_max,
			 budget_currency, budget_raw, location, skills, tags, description,
			 client_info, posted_at, raw, status, closed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		ON CONFLICT (dedup_hash) DO UPDATE
			SET last_seen_at = NOW(),
			    -- closed/merged is a terminal state for ingest; only enricher (UpdateStatus) can promote between non-open states
			    status = CASE WHEN hunt_freelance.status = 'open' THEN EXCLUDED.status ELSE hunt_freelance.status END,
			    closed_at = COALESCE(hunt_freelance.closed_at, EXCLUDED.closed_at)
		RETURNING id, (xmax = 0) AS created`,
		f.DedupHash, f.Title, f.URL, f.Platform, f.Source,
		nullInt(f.BudgetMin), nullInt(f.BudgetMax),
		nullStr(f.BudgetCurrency), nullStr(f.BudgetRaw), nullStr(f.Location),
		nullSlice(f.Skills), nullSlice(f.Tags), nullStr(f.Description),
		nullStr(f.ClientInfo), f.PostedAt, nullRaw(f.Raw), status, f.ClosedAt,
	).Scan(&id, &created)
	if err != nil {
		return 0, OutcomeError, fmt.Errorf("hunt: upsert freelance: %w", err)
	}
	if created {
		// Only notify for open freelance projects — non-open status means archived listing.
		if s.notifier != nil && status == StatusOpen {
			s.notifier.NotifyNewFreelance(f)
		}
		return id, OutcomeCreated, nil
	}
	return id, OutcomeMerged, nil
}

// GetFreelance returns a single freelance project by id; ErrNotFound if missing.
func (s *Store) GetFreelance(ctx context.Context, id int64) (*Freelance, error) {
	var f Freelance
	var location, budCur, budRaw, desc, clientInfo *string
	var budMin, budMax *int
	err := s.pool.QueryRow(ctx, `
		SELECT id, dedup_hash, title, url, platform, source, budget_min, budget_max,
		       budget_currency, budget_raw, location, skills, tags, description,
		       client_info, posted_at, first_seen_at, last_seen_at,
		       status, closed_at, last_checked_at
		FROM hunt_freelance WHERE id = $1`, id).Scan(
		&f.ID, &f.DedupHash, &f.Title, &f.URL, &f.Platform, &f.Source,
		&budMin, &budMax, &budCur, &budRaw, &location,
		&f.Skills, &f.Tags, &desc, &clientInfo, &f.PostedAt,
		&f.FirstSeenAt, &f.LastSeenAt,
		&f.Status, &f.ClosedAt, &f.LastCheckedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("hunt: get freelance: %w", err)
	}
	if location != nil {
		f.Location = *location
	}
	if budCur != nil {
		f.BudgetCurrency = *budCur
	}
	if budRaw != nil {
		f.BudgetRaw = *budRaw
	}
	if desc != nil {
		f.Description = *desc
	}
	if clientInfo != nil {
		f.ClientInfo = *clientInfo
	}
	if budMin != nil {
		f.BudgetMin = *budMin
	}
	if budMax != nil {
		f.BudgetMax = *budMax
	}
	return &f, nil
}

// UpsertSecurity inserts a new security program or updates last_seen_at on conflict.
// Status is preserved on conflict: once archived, a re-ingest with "open" does NOT revert it.
func (s *Store) UpsertSecurity(ctx context.Context, sec Security) (id int64, outcome Outcome, err error) {
	status := sec.Status
	if status == "" {
		status = StatusOpen
	}
	var created bool
	err = s.pool.QueryRow(ctx, `
		INSERT INTO hunt_security
			(dedup_hash, name, url, platform, program_type, min_bounty, max_bounty,
			 targets, managed, description, raw, status, closed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (dedup_hash) DO UPDATE
			SET last_seen_at = NOW(),
			    -- closed/merged is a terminal state for ingest; only enricher (UpdateStatus) can promote between non-open states
			    status = CASE WHEN hunt_security.status = 'open' THEN EXCLUDED.status ELSE hunt_security.status END,
			    closed_at = COALESCE(hunt_security.closed_at, EXCLUDED.closed_at)
		RETURNING id, (xmax = 0) AS created`,
		sec.DedupHash, sec.Name, sec.URL, sec.Platform,
		nullStr(sec.ProgramType), nullInt(sec.MinBounty), nullInt(sec.MaxBounty),
		nullSlice(sec.Targets), sec.Managed, nullStr(sec.Description), nullRaw(sec.Raw),
		status, sec.ClosedAt,
	).Scan(&id, &created)
	if err != nil {
		return 0, OutcomeError, fmt.Errorf("hunt: upsert security: %w", err)
	}
	if created {
		// Only notify for open security programs — non-open status means archived program.
		if s.notifier != nil && status == StatusOpen {
			s.notifier.NotifyNewSecurity(sec)
		}
		return id, OutcomeCreated, nil
	}
	return id, OutcomeMerged, nil
}

// GetSecurity returns a single security program by id; ErrNotFound if missing.
func (s *Store) GetSecurity(ctx context.Context, id int64) (*Security, error) {
	var sec Security
	var progType, desc *string
	var minB, maxB *int
	err := s.pool.QueryRow(ctx, `
		SELECT id, dedup_hash, name, url, platform, program_type, min_bounty, max_bounty,
		       targets, managed, description, first_seen_at, last_seen_at,
		       status, closed_at, last_checked_at
		FROM hunt_security WHERE id = $1`, id).Scan(
		&sec.ID, &sec.DedupHash, &sec.Name, &sec.URL, &sec.Platform,
		&progType, &minB, &maxB, &sec.Targets, &sec.Managed, &desc,
		&sec.FirstSeenAt, &sec.LastSeenAt,
		&sec.Status, &sec.ClosedAt, &sec.LastCheckedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("hunt: get security: %w", err)
	}
	if progType != nil {
		sec.ProgramType = *progType
	}
	if desc != nil {
		sec.Description = *desc
	}
	if minB != nil {
		sec.MinBounty = *minB
	}
	if maxB != nil {
		sec.MaxBounty = *maxB
	}
	return &sec, nil
}

// UpsertAuditContest inserts a new audit contest or updates last_seen_at on conflict.
func (s *Store) UpsertAuditContest(ctx context.Context, ac AuditContest) (id int64, outcome Outcome, err error) {
	var created bool
	err = s.pool.QueryRow(ctx, `
		INSERT INTO hunt_audit_contests
			(dedup_hash, title, url, platform, total_pool, currency,
			 starts_at, ends_at, languages, description, raw)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (dedup_hash) DO UPDATE
			SET last_seen_at = NOW()
		RETURNING id, (xmax = 0) AS created`,
		ac.DedupHash, ac.Title, ac.URL, ac.Platform,
		nullInt(ac.TotalPool), nullStr(ac.Currency),
		ac.StartsAt, ac.EndsAt,
		nullSlice(ac.Languages), nullStr(ac.Description), nullRaw(ac.Raw),
	).Scan(&id, &created)
	if err != nil {
		return 0, OutcomeError, fmt.Errorf("hunt: upsert audit_contest: %w", err)
	}
	if created {
		return id, OutcomeCreated, nil
	}
	return id, OutcomeMerged, nil
}

// GetAuditContest returns a single audit contest by id; ErrNotFound if missing.
func (s *Store) GetAuditContest(ctx context.Context, id int64) (*AuditContest, error) {
	var ac AuditContest
	var currency, desc *string
	var pool *int
	err := s.pool.QueryRow(ctx, `
		SELECT id, dedup_hash, title, url, platform, total_pool, currency,
		       starts_at, ends_at, languages, description, first_seen_at, last_seen_at
		FROM hunt_audit_contests WHERE id = $1`, id).Scan(
		&ac.ID, &ac.DedupHash, &ac.Title, &ac.URL, &ac.Platform,
		&pool, &currency, &ac.StartsAt, &ac.EndsAt,
		&ac.Languages, &desc, &ac.FirstSeenAt, &ac.LastSeenAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("hunt: get audit_contest: %w", err)
	}
	if currency != nil {
		ac.Currency = *currency
	}
	if desc != nil {
		ac.Description = *desc
	}
	if pool != nil {
		ac.TotalPool = *pool
	}
	return &ac, nil
}

// Rate inserts or updates a user rating for a hunt entry.
// Upserts on (entry_kind, entry_id, user_name).
func (s *Store) Rate(ctx context.Context, kind string, entryID int64, user, stage, note string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO hunt_ratings (entry_kind, entry_id, user_name, stage, note, rated_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW(), NOW())
		ON CONFLICT (entry_kind, entry_id, user_name) DO UPDATE
			SET stage      = EXCLUDED.stage,
			    note       = EXCLUDED.note,
			    updated_at = NOW()`,
		kind, entryID, user, stage, nullStr(note),
	)
	if err != nil {
		return fmt.Errorf("hunt: rate: %w", err)
	}
	return nil
}

// GetRating returns the rating for a specific (kind, entryID, user) triple.
// Returns ErrNotFound if no rating exists.
func (s *Store) GetRating(ctx context.Context, kind string, entryID int64, user string) (*Rating, error) {
	var r Rating
	var note *string
	err := s.pool.QueryRow(ctx, `
		SELECT id, entry_kind, entry_id, user_name, stage, note, rated_at, updated_at
		FROM hunt_ratings
		WHERE entry_kind = $1 AND entry_id = $2 AND user_name = $3`,
		kind, entryID, user,
	).Scan(&r.ID, &r.EntryKind, &r.EntryID, &r.UserName, &r.Stage, &note, &r.RatedAt, &r.UpdatedAt)
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

// GetBountyWithRaw returns a single bounty by id including the Raw JSONB column.
// Use this for audit/debug scenarios where raw source data is needed.
func (s *Store) GetBountyWithRaw(ctx context.Context, id int64) (*Bounty, error) {
	var b Bounty
	var org, currency, desc *string
	var amountCents *int64
	var issueNum *int
	var relevance *float32
	err := s.pool.QueryRow(ctx, `
		SELECT id, dedup_hash, title, url, org, source, amount_cents, currency,
		       issue_number, skills, description, relevance, posted_at,
		       first_seen_at, last_seen_at, raw, status, closed_at, last_checked_at
		FROM hunt_bounties WHERE id = $1`, id).Scan(
		&b.ID, &b.DedupHash, &b.Title, &b.URL,
		&org, &b.Source, &amountCents, &currency,
		&issueNum, &b.Skills, &desc, &relevance, &b.PostedAt,
		&b.FirstSeenAt, &b.LastSeenAt, &b.Raw,
		&b.Status, &b.ClosedAt, &b.LastCheckedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("hunt: get bounty with raw: %w", err)
	}
	if org != nil {
		b.Org = *org
	}
	if currency != nil {
		b.Currency = *currency
	}
	if desc != nil {
		b.Description = *desc
	}
	if amountCents != nil {
		b.AmountCents = *amountCents
	}
	if issueNum != nil {
		b.IssueNumber = *issueNum
	}
	if relevance != nil {
		b.Relevance = *relevance
	}
	return &b, nil
}

// FreelanceFilter narrows ListFreelance results.
type FreelanceFilter struct {
	Platform      string
	MinBudget     int
	Skills        []string
	IncludeClosed bool // when false (default), only status='open' rows are returned
	Limit         int  // default 50, max 500
	Offset        int
}

// ListFreelance returns freelance projects newest-first with optional filters.
// By default, only status='open' rows are returned. Set IncludeClosed=true for all.
func (s *Store) ListFreelance(ctx context.Context, f FreelanceFilter) ([]Freelance, error) {
	limit := clampLimit(f.Limit, 50, 500)

	conds := []string{}
	args := []any{}
	argN := 1

	if !f.IncludeClosed {
		conds = append(conds, "status = 'open'")
	}
	if f.Platform != "" {
		conds = append(conds, fmt.Sprintf("platform = $%d", argN))
		args = append(args, f.Platform)
		argN++
	}
	if f.MinBudget > 0 {
		conds = append(conds, fmt.Sprintf("budget_max >= $%d", argN))
		args = append(args, f.MinBudget)
		argN++
	}
	if len(f.Skills) > 0 {
		conds = append(conds, fmt.Sprintf("skills && $%d::text[]", argN))
		args = append(args, f.Skills)
		argN++
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	args = append(args, limit, f.Offset)
	q := fmt.Sprintf(`
		SELECT id, dedup_hash, title, url, platform, source, budget_min, budget_max,
		       budget_currency, budget_raw, location, skills, tags, description,
		       client_info, posted_at, first_seen_at, last_seen_at,
		       status, closed_at, last_checked_at
		FROM hunt_freelance
		%s
		ORDER BY last_seen_at DESC
		LIMIT $%d OFFSET $%d`, where, argN, argN+1)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("hunt: list freelance: %w", err)
	}
	defer rows.Close()

	var result []Freelance
	for rows.Next() {
		var fl Freelance
		var location, budCur, budRaw, desc, clientInfo *string
		var budMin, budMax *int
		if err := rows.Scan(
			&fl.ID, &fl.DedupHash, &fl.Title, &fl.URL, &fl.Platform, &fl.Source,
			&budMin, &budMax, &budCur, &budRaw, &location,
			&fl.Skills, &fl.Tags, &desc, &clientInfo, &fl.PostedAt,
			&fl.FirstSeenAt, &fl.LastSeenAt,
			&fl.Status, &fl.ClosedAt, &fl.LastCheckedAt,
		); err != nil {
			return nil, fmt.Errorf("hunt: list freelance scan: %w", err)
		}
		if location != nil {
			fl.Location = *location
		}
		if budCur != nil {
			fl.BudgetCurrency = *budCur
		}
		if budRaw != nil {
			fl.BudgetRaw = *budRaw
		}
		if desc != nil {
			fl.Description = *desc
		}
		if clientInfo != nil {
			fl.ClientInfo = *clientInfo
		}
		if budMin != nil {
			fl.BudgetMin = *budMin
		}
		if budMax != nil {
			fl.BudgetMax = *budMax
		}
		result = append(result, fl)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hunt: list freelance rows: %w", err)
	}
	return result, nil
}

// SecurityFilter narrows ListSecurity results.
type SecurityFilter struct {
	Platform      string
	MinBounty     int
	IncludeClosed bool // when false (default), only status='open' rows are returned
	Limit         int  // default 50, max 500
	Offset        int
}

// ListSecurity returns security programs newest-first with optional filters.
// By default, only status='open' rows are returned. Set IncludeClosed=true for all.
func (s *Store) ListSecurity(ctx context.Context, f SecurityFilter) ([]Security, error) {
	limit := clampLimit(f.Limit, 50, 500)

	conds := []string{}
	args := []any{}
	argN := 1

	if !f.IncludeClosed {
		conds = append(conds, "status = 'open'")
	}
	if f.Platform != "" {
		conds = append(conds, fmt.Sprintf("platform = $%d", argN))
		args = append(args, f.Platform)
		argN++
	}
	if f.MinBounty > 0 {
		conds = append(conds, fmt.Sprintf("max_bounty >= $%d", argN))
		args = append(args, f.MinBounty)
		argN++
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	args = append(args, limit, f.Offset)
	q := fmt.Sprintf(`
		SELECT id, dedup_hash, name, url, platform, program_type, min_bounty, max_bounty,
		       targets, managed, description, first_seen_at, last_seen_at,
		       status, closed_at, last_checked_at
		FROM hunt_security
		%s
		ORDER BY last_seen_at DESC
		LIMIT $%d OFFSET $%d`, where, argN, argN+1)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("hunt: list security: %w", err)
	}
	defer rows.Close()

	var result []Security
	for rows.Next() {
		var sec Security
		var progType, desc *string
		var minB, maxB *int
		if err := rows.Scan(
			&sec.ID, &sec.DedupHash, &sec.Name, &sec.URL, &sec.Platform,
			&progType, &minB, &maxB, &sec.Targets, &sec.Managed, &desc,
			&sec.FirstSeenAt, &sec.LastSeenAt,
			&sec.Status, &sec.ClosedAt, &sec.LastCheckedAt,
		); err != nil {
			return nil, fmt.Errorf("hunt: list security scan: %w", err)
		}
		if progType != nil {
			sec.ProgramType = *progType
		}
		if desc != nil {
			sec.Description = *desc
		}
		if minB != nil {
			sec.MinBounty = *minB
		}
		if maxB != nil {
			sec.MaxBounty = *maxB
		}
		result = append(result, sec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hunt: list security rows: %w", err)
	}
	return result, nil
}

// AuditContestFilter narrows ListAuditContests results.
type AuditContestFilter struct {
	Platform string
	MinPool  int
	Limit    int // default 50, max 500
	Offset   int
}

// ListAuditContests returns audit contests newest-first with optional filters.
func (s *Store) ListAuditContests(ctx context.Context, f AuditContestFilter) ([]AuditContest, error) {
	limit := clampLimit(f.Limit, 50, 500)

	conds := []string{}
	args := []any{}
	argN := 1

	if f.Platform != "" {
		conds = append(conds, fmt.Sprintf("platform = $%d", argN))
		args = append(args, f.Platform)
		argN++
	}
	if f.MinPool > 0 {
		conds = append(conds, fmt.Sprintf("total_pool >= $%d", argN))
		args = append(args, f.MinPool)
		argN++
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	args = append(args, limit, f.Offset)
	q := fmt.Sprintf(`
		SELECT id, dedup_hash, title, url, platform, total_pool, currency,
		       starts_at, ends_at, languages, description, first_seen_at, last_seen_at
		FROM hunt_audit_contests
		%s
		ORDER BY last_seen_at DESC
		LIMIT $%d OFFSET $%d`, where, argN, argN+1)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("hunt: list audit_contests: %w", err)
	}
	defer rows.Close()

	var result []AuditContest
	for rows.Next() {
		var ac AuditContest
		var currency, desc *string
		var pool *int
		if err := rows.Scan(
			&ac.ID, &ac.DedupHash, &ac.Title, &ac.URL, &ac.Platform,
			&pool, &currency, &ac.StartsAt, &ac.EndsAt,
			&ac.Languages, &desc, &ac.FirstSeenAt, &ac.LastSeenAt,
		); err != nil {
			return nil, fmt.Errorf("hunt: list audit_contests scan: %w", err)
		}
		if currency != nil {
			ac.Currency = *currency
		}
		if desc != nil {
			ac.Description = *desc
		}
		if pool != nil {
			ac.TotalPool = *pool
		}
		result = append(result, ac)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hunt: list audit_contests rows: %w", err)
	}
	return result, nil
}

// RatingFilter narrows ListRatings results.
type RatingFilter struct {
	Kind   string // entry_kind: bounty, job, freelance, security, audit_contest
	Stage  string // e.g. "interesting", "saved"
	User   string // user_name filter
	Limit  int    // default 50, max 500
	Offset int
}

// ListRatings returns ratings newest-updated-first with optional filters.
func (s *Store) ListRatings(ctx context.Context, f RatingFilter) ([]Rating, error) {
	limit := clampLimit(f.Limit, 50, 500)

	conds := []string{}
	args := []any{}
	argN := 1

	if f.Kind != "" {
		conds = append(conds, fmt.Sprintf("entry_kind = $%d", argN))
		args = append(args, f.Kind)
		argN++
	}
	if f.Stage != "" {
		conds = append(conds, fmt.Sprintf("stage = $%d", argN))
		args = append(args, f.Stage)
		argN++
	}
	if f.User != "" {
		conds = append(conds, fmt.Sprintf("user_name = $%d", argN))
		args = append(args, f.User)
		argN++
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	args = append(args, limit, f.Offset)
	q := fmt.Sprintf(`
		SELECT id, entry_kind, entry_id, user_name, stage, note, rated_at, updated_at
		FROM hunt_ratings
		%s
		ORDER BY updated_at DESC
		LIMIT $%d OFFSET $%d`, where, argN, argN+1)

	rows, err := s.pool.Query(ctx, q, args...)
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
			&r.Stage, &note, &r.RatedAt, &r.UpdatedAt,
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

// --- status enrichment methods ---

// UpdateStatus sets the status, closed_at and last_checked_at for a single entry.
// kind must be one of the KindXxx constants ("bounty", "job", "freelance", "security").
func (s *Store) UpdateStatus(ctx context.Context, kind string, id int64, status string, closedAt *time.Time) error {
	table, err := kindTable(kind)
	if err != nil {
		return err
	}
	_, execErr := s.pool.Exec(ctx,
		"UPDATE "+table+" SET status=$1, closed_at=$2, last_checked_at=NOW() WHERE id=$3",
		status, closedAt, id,
	)
	if execErr != nil {
		return fmt.Errorf("hunt: update status (%s id=%d): %w", kind, id, execErr)
	}
	return nil
}

// UpdateStatusBatch applies multiple status updates for a given kind in a single transaction.
func (s *Store) UpdateStatusBatch(ctx context.Context, kind string, updates []StatusUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	table, err := kindTable(kind)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("hunt: begin batch update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, u := range updates {
		if _, execErr := tx.Exec(ctx,
			"UPDATE "+table+" SET status=$1, closed_at=$2, last_checked_at=NOW() WHERE id=$3",
			u.Status, u.ClosedAt, u.ID,
		); execErr != nil {
			return fmt.Errorf("hunt: batch update status (%s id=%d): %w", kind, u.ID, execErr)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("hunt: commit batch update: %w", err)
	}
	return nil
}

// GetBountiesNeedingCheck returns open bounties whose last_checked_at is NULL
// or older than maxAge, ordered NULLS FIRST. Limit caps the result set.
func (s *Store) GetBountiesNeedingCheck(ctx context.Context, maxAge time.Duration, limit int) ([]Bounty, error) {
	limit = clampLimit(limit, 50, 500)
	cutoff := time.Now().Add(-maxAge)
	rows, err := s.pool.Query(ctx, `
		SELECT id, dedup_hash, title, url, org, source, amount_cents, currency,
		       issue_number, skills, description, relevance, posted_at,
		       first_seen_at, last_seen_at, status, closed_at, last_checked_at
		FROM hunt_bounties
		WHERE status = 'open'
		  AND (last_checked_at IS NULL OR last_checked_at < $1)
		ORDER BY last_checked_at NULLS FIRST
		LIMIT $2`, cutoff, limit)
	if err != nil {
		return nil, fmt.Errorf("hunt: get bounties needing check: %w", err)
	}
	defer rows.Close()

	var result []Bounty
	for rows.Next() {
		b, scanErr := scanBountyRow(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("hunt: get bounties needing check scan: %w", scanErr)
		}
		result = append(result, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hunt: get bounties needing check rows: %w", err)
	}
	return result, nil
}

// kindTable maps a KindXxx string to the corresponding table name.
func kindTable(kind string) (string, error) {
	switch kind {
	case KindBounty:
		return "hunt_bounties", nil
	case KindJob:
		return "hunt_jobs", nil
	case KindFreelance:
		return "hunt_freelance", nil
	case KindSecurity:
		return "hunt_security", nil
	default:
		return "", fmt.Errorf("hunt: unknown kind %q", kind)
	}
}

// --- row scanner helpers ---

// bountyScanner is the subset of pgx.Rows used by scanBountyRow.
// Using an interface avoids importing pgx in callers and enables testing.
type bountyScanner interface {
	Scan(dest ...any) error
}

// scanBountyRow scans one row from hunt_bounties (full projection including status columns).
// Centralizes the nullable-field unwrapping that was previously copy-pasted across
// ListBounties, GetBountiesNeedingCheck, and similar selects.
func scanBountyRow(row bountyScanner) (Bounty, error) {
	var b Bounty
	var org, currency, desc *string
	var amountCents *int64
	var issueNum *int
	var relevance *float32
	if err := row.Scan(
		&b.ID, &b.DedupHash, &b.Title, &b.URL,
		&org, &b.Source, &amountCents, &currency,
		&issueNum, &b.Skills, &desc, &relevance, &b.PostedAt,
		&b.FirstSeenAt, &b.LastSeenAt,
		&b.Status, &b.ClosedAt, &b.LastCheckedAt,
	); err != nil {
		return Bounty{}, err
	}
	if org != nil {
		b.Org = *org
	}
	if currency != nil {
		b.Currency = *currency
	}
	if desc != nil {
		b.Description = *desc
	}
	if amountCents != nil {
		b.AmountCents = *amountCents
	}
	if issueNum != nil {
		b.IssueNumber = *issueNum
	}
	if relevance != nil {
		b.Relevance = *relevance
	}
	return b, nil
}

// --- nullable helpers ---

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func nullInt64(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

func nullFloat32(f float32) any {
	if f == 0 {
		return nil
	}
	return f
}

func nullSlice(ss []string) any {
	if len(ss) == 0 {
		return nil
	}
	return ss
}

func nullRaw(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

func clampLimit(v, def, max int) int {
	if v <= 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
}
