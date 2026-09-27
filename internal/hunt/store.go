package hunt

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/anatolykoptev/go-kit/pgutil"
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

	// enrichSem bounds concurrent detached enrichment goroutines (#184 fix).
	// Without this, rapid ListBounties calls spawn unbounded goroutines that
	// each hold a 30s detached context — under heavy admin UI / MCP usage they
	// accumulate faster than they complete. The semaphore caps concurrency to
	// enrichMaxConcurrent; excess calls skip enrichment (best-effort, non-blocking).
	enrichSem chan struct{}
}

// enrichMaxConcurrent caps the number of detached enrichment goroutines that
// ListBounties may spawn. Each holds a 30s detached context; without a cap,
// rapid calls (admin UI refresh, MCP tool fan-out) can pile up goroutines
// faster than they complete (#184).
const enrichMaxConcurrent = 5

// onEnrichSkipped is called when the enrichment semaphore is full and
// enrichment is skipped. Set by engine.Init via SetEnrichSkipHook so the
// hunt package doesn't need to import engine (avoids circular dependency).
// OBS-6: makes "semaphore full" events visible in Prometheus.
var onEnrichSkipped func()

// SetEnrichSkipHook wires the enrichment-skip callback. Called from engine.Init.
func SetEnrichSkipHook(fn func()) { onEnrichSkipped = fn }

// NewStore returns a Store using the given pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{
		pool:      pool,
		enrichSem: make(chan struct{}, enrichMaxConcurrent),
	}
}

// Pool returns the underlying pgxpool.Pool used by this Store.
// Callers that need direct pool access (e.g. score.LoadProfile) should use this
// accessor rather than coupling to the internal field.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// SetEnricher wires a background GitHub enricher that triggers on ListBounties reads.
func (s *Store) SetEnricher(e BountyEnricher) { s.enricher = e }

// SetNotifier wires a Telegram notifier that fires on OutcomeCreated ingest events.
func (s *Store) SetNotifier(n Notifier) { s.notifier = n }

// Notifier returns the wired Telegram notifier (may be nil).
// Used by the persist layer (opportunity_search.go) to apply the
// backfill-guard notify policy outside of the upsert internals.
func (s *Store) Notifier() Notifier { return s.notifier }

// NotifyJobIfOpen fires NotifyNewJob on the wired notifier for an open job.
// It is a no-op if the notifier is nil or the job is not open/empty-status.
// Called by the MCP path (persistJobListings) when HUNT_NOTIFY_ON_SEARCH=true.
// score is nil because the MCP path scores lazily (Decision 5) — the worker's
// next cycle picks up rows with no account_job_scores entry (per-account
// unscored sweep, P2).
func (s *Store) NotifyJobIfOpen(j Job) {
	if s.notifier != nil && (j.Status == StatusOpen || j.Status == "") {
		s.notifier.NotifyNewJob(j, nil)
	}
}

// Migrate runs schema migrations in lexical order via go-kit/pgutil.
// Idempotent: already-applied files are skipped; on an empty tracking table
// the Baseline predicate adopts an existing prod DB (marks all files applied
// without re-running them) so the cutover from the old schema_versions tracker
// never re-runs a migration against a live DB.
//
// pgutil owns its own `schema_migrations(name, checksum, applied_at)` table;
// the legacy `schema_versions(version, applied_at)` table from BH-8 is left
// orphaned on existing DBs (harmless) and is no longer created on fresh ones.
func (s *Store) Migrate(ctx context.Context) error {
	schemaSub, err := fs.Sub(schemaFS, "schema")
	if err != nil {
		return fmt.Errorf("hunt: schema sub fs: %w", err)
	}
	return pgutil.RunMigrations(ctx, s.pool, schemaSub, pgutil.MigrateOptions{
		// PreMigrate preserves the old behaviour: ensure search_path is public
		// before any DDL runs (pgutil is schema-qualified but the migrations
		// themselves use unqualified names).
		PreMigrate: func(ctx context.Context, conn *pgxpool.Conn) error {
			if _, err := conn.Exec(ctx, "SET search_path TO public"); err != nil {
				return fmt.Errorf("hunt: set search_path: %w", err)
			}
			return nil
		},
		// Baseline: adopt an already-migrated prod DB. The old tracker
		// (schema_versions) existing AND non-empty means this DB was brought
		// up by the pre-pgutil Migrate; mark every file applied without
		// executing so no DDL re-runs against the live schema. On a fresh DB
		// to_regclass('public.schema_versions') IS NULL → false → pgutil
		// applies all files in order.
		//
		// Implemented as two steps (not a single `to_regclass(...) IS NOT NULL
		// AND EXISTS(...)` expression) because SQL AND is not guaranteed to
		// short-circuit: Postgres will plan/evaluate the EXISTS subquery even
		// when the left operand is false, which errors with "relation
		// schema_versions does not exist" on a fresh DB. Guarding the table
		// scan behind the to_regclass check in Go makes the predicate actually
		// defensive, matching the spec's intent.
		// CUTOVER SAFETY (pr-council #309, finding 3): do NOT add a NEW
		// schema/*.sql file in the same PR that first deploys pgutil to a DB
		// whose legacy schema_versions is populated. Baseline marks ALL
		// discovered files applied WITHOUT running their DDL, so a brand-new
		// file would be silently skipped. New migrations must land in a LATER
		// PR, after schema_migrations is already populated.
		Baseline: func(ctx context.Context, conn *pgxpool.Conn) (bool, error) {
			var exists bool
			if err := conn.QueryRow(ctx,
				`SELECT to_regclass('public.schema_versions') IS NOT NULL`,
			).Scan(&exists); err != nil {
				return false, fmt.Errorf("hunt: baseline probe (regclass): %w", err)
			}
			if !exists {
				return false, nil
			}
			var populated bool
			if err := conn.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM schema_versions LIMIT 1)`,
			).Scan(&populated); err != nil {
				return false, fmt.Errorf("hunt: baseline probe (rows): %w", err)
			}
			return populated, nil
		},
	})
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
			    closed_at = COALESCE(hunt_jobs.closed_at, EXCLUDED.closed_at),
			    -- Fill-only: promote empty/weak-row fields from a newer ingest but never clobber
			    -- an already-good value. title='' is the queryable proxy for weak-ingest
			    -- rows (LLM returned nothing) and the marker for rows needing re-enrichment;
			    -- no additional migration column is required. Content fields also promote when
			    -- the stored value is empty/NULL and the incoming value is non-empty. The
			    -- EXCLUDED.* <> '' / IS NOT NULL guards prevent an empty re-ingest
			    -- from clobbering a populated field even on a weak row (title='' stored).
			    -- Never downgrades good->weak.
			    title       = CASE WHEN hunt_jobs.title = '' THEN EXCLUDED.title ELSE hunt_jobs.title END,
			    description = CASE WHEN (hunt_jobs.title = '' OR hunt_jobs.description IS NULL OR hunt_jobs.description = '') AND EXCLUDED.description IS NOT NULL AND EXCLUDED.description <> '' THEN EXCLUDED.description ELSE hunt_jobs.description END,
			    company     = CASE WHEN (hunt_jobs.title = '' OR hunt_jobs.company IS NULL OR hunt_jobs.company = '') AND EXCLUDED.company IS NOT NULL AND EXCLUDED.company <> '' THEN EXCLUDED.company ELSE hunt_jobs.company END,
			    skills      = CASE WHEN (hunt_jobs.title = '' OR array_length(hunt_jobs.skills, 1) IS NULL) AND array_length(EXCLUDED.skills, 1) IS NOT NULL THEN EXCLUDED.skills ELSE hunt_jobs.skills END
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
		j, scanErr := scanJobRow(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("hunt: list jobs scan: %w", scanErr)
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

// jobScanner is the subset of pgx.Rows used by scanJobRow.
// Using an interface avoids importing pgx in callers and enables testing.
type jobScanner interface {
	Scan(dest ...any) error
}

// scanJobRow scans one hunt_jobs row (full projection including status columns).
// Centralises the nullable-field unwrapping for the two pgx.Rows-based callers:
// ListJobs and UnscoredOpenJobs. GetJob uses an inline pgx.QueryRow scan and is
// NOT routed through this helper. The caller must provide a row that was queried
// with the canonical column order (id … last_checked_at).
func scanJobRow(row jobScanner) (Job, error) {
	var j Job
	var company, extID, location, remote, jobType, exp, cur, interval, desc *string
	var salMin, salMax *int
	if err := row.Scan(
		&j.ID, &j.DedupHash, &j.Title, &company, &j.URL, &j.Source,
		&extID, &location, &remote, &jobType, &exp,
		&salMin, &salMax, &cur, &interval,
		&j.Skills, &j.Tags, &desc, &j.PostedAt,
		&j.FirstSeenAt, &j.LastSeenAt,
		&j.Status, &j.ClosedAt, &j.LastCheckedAt,
	); err != nil {
		return Job{}, err
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
	return j, nil
}

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

// CountOpenJobs returns the number of hunt_jobs rows with status='open'.
// Errors are silently swallowed (returns 0) — callers use this for low-stakes
// nav badge counts where a DB error should not break page rendering.
func (s *Store) CountOpenJobs(ctx context.Context) int {
	var n int
	_ = s.pool.QueryRow(ctx, "SELECT count(*) FROM hunt_jobs WHERE status = 'open'").Scan(&n)
	return n
}

// stageIn returns true when stage is present in the given slice.
func stageIn(stage string, stages []string) bool {
	for _, s := range stages {
		if s == stage {
			return true
		}
	}
	return false
}

// SetStatus updates hunt_jobs.status and keeps closed_at in lockstep with the
// enricher's UpdateStatus invariant:
//   - terminal status (closed/merged/archived/ended): stamp closed_at=NOW() only
//     when it is currently NULL (do not overwrite an existing close timestamp).
//   - open: reset closed_at=NULL.
//
// Returns ErrNotFound when no row with the given id exists.
// Use this for operator-driven lifecycle changes from the admin UI; distinct from
// UpdateStatus (enricher-driven, accepts an explicit closedAt pointer).
func (s *Store) SetStatus(ctx context.Context, id int64, status string) error {
	// closed_at logic mirrors UpdateStatus: terminal → stamp if NULL, open → clear.
	const q = `
UPDATE hunt_jobs
   SET status    = $1,
       closed_at = CASE
                     WHEN $1 = 'open'
                       THEN NULL
                     WHEN closed_at IS NULL
                       THEN NOW()
                     ELSE closed_at
                   END
 WHERE id = $2`
	tag, err := s.pool.Exec(ctx, q, status, id)
	if err != nil {
		return fmt.Errorf("hunt: set status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("hunt: set status: %w", ErrNotFound)
	}
	return nil
}

// CountBySource returns the open job count per source ordered descending by count.
// Errors and empty results both return nil.
func (s *Store) CountBySource(ctx context.Context) []SourceCount {
	rows, err := s.pool.Query(ctx,
		"SELECT source, count(*) FROM hunt_jobs WHERE status = 'open' GROUP BY source ORDER BY 2 DESC",
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var result []SourceCount
	for rows.Next() {
		var sc SourceCount
		if err := rows.Scan(&sc.Source, &sc.N); err != nil {
			continue
		}
		result = append(result, sc)
	}
	return result
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
