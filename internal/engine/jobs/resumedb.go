package jobs

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"github.com/anatolykoptev/go-kit/env"
	"github.com/anatolykoptev/go-kit/pgutil"
	"github.com/anatolykoptev/go-kit/retry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema/*.sql
var schemaFS embed.FS

// ageSetup runs per-connection AGE initialization. No LOAD 'age' is issued:
// every graph call site uses the fully-qualified ag_catalog.cypher(...), and age
// is in shared_preload_libraries, so cypher() is already available without LOAD.
// LOAD is also disallowed for non-superuser roles such as the gojob app role,
// so issuing it would break least-privilege / own-DB deploys.
const ageSetup = `SET search_path TO ag_catalog, "$user", public`

// Package-level singletons, set from main.go.
var resumeDB *ResumeDB

// SetResumeDB sets the package-level resume DB instance.
func SetResumeDB(db *ResumeDB) { resumeDB = db }

// GetResumeDB returns the package-level resume DB instance (may be nil).
func GetResumeDB() *ResumeDB { return resumeDB }

// ResumeDB holds the pgx connection pool for resume storage.
type ResumeDB struct {
	pool *pgxpool.Pool
}

// ConnectResumeDB creates a pgx pool and runs schema migrations.
func ConnectResumeDB(ctx context.Context, databaseURL string) (*ResumeDB, error) {
	if databaseURL == "" {
		return nil, errors.New("DATABASE_URL is required")
	}

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	// #172 fix: DB pool size is env-overridable. Default 25 (raised from 10
	// in BH-3) — 10 was too small under concurrent load (17 parallel connectors
	// + ATS ingest + admin UI + MCP tools all share one pool).
	// DATABASE_MAX_CONNS lets ops tune without a rebuild.
	maxConns := env.MustInt("DATABASE_MAX_CONNS", 25)
	if maxConns < 1 {
		maxConns = 1
	}
	config.MaxConns = int32(maxConns) //nolint:gosec // G115: bounded to [1, MaxInt32] by env.MustInt + guard above
	config.MinConns = 1

	// Force search_path to public for all pool connections.
	// Some Postgres roles have ag_catalog first in search_path, which causes
	// CREATE TABLE / INSERT to resolve to ag_catalog instead of public.
	// AGE graph queries explicitly set their own search_path via ageSetup.
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET search_path TO public")
		return err
	}

	pool, err := retry.Do(ctx, retry.Options{
		MaxAttempts:  10,
		InitialDelay: time.Second,
		MaxDelay:     30 * time.Second,
		Jitter:       true,
		OnRetry: func(attempt int, err error) {
			slog.Warn("postgres not ready, retrying", slog.Int("attempt", attempt), slog.Any("error", err))
		},
	}, func() (*pgxpool.Pool, error) {
		p, retryErr := pgxpool.NewWithConfig(ctx, config)
		if retryErr != nil {
			return nil, retryErr
		}
		if retryErr = p.Ping(ctx); retryErr != nil {
			p.Close()
			return nil, retryErr
		}
		return p, nil
	})
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}

	db := &ResumeDB{pool: pool}
	if err := db.runMigrations(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	// Detect whether migration 005 created the embedding column (soft — may be absent).
	if err := db.DetectEmbeddingColumn(ctx); err != nil {
		slog.Warn("could not detect embedding column", slog.Any("error", err))
	} else {
		slog.Info("resume_vectors embedding column", slog.Bool("present", db.HasEmbedding()))
	}

	slog.Info("resume postgres connected", slog.String("addr", config.ConnConfig.Host))
	return db, nil
}

func (db *ResumeDB) Close() {
	db.pool.Close()
}

// Pool returns the underlying pgx connection pool for shared use (e.g. oversize store).
func (db *ResumeDB) Pool() *pgxpool.Pool {
	return db.pool
}

// execConn is the common subset of *pgxpool.Pool and pgx.Tx that the resume
// write/read methods use. It lets a method run against either the pool (the
// normal path) or a transaction (the atomic rebuild path) without changing its
// signature: the caller threads a context carrying the tx (see withTx) and
// conn(ctx) returns the tx in place of the pool.
type execConn interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// txCtxKey is the context key under which BuildMasterResume publishes its
// transaction so the tx-aware write methods (conn) pick it up.
type txCtxKey struct{}

// withTx returns a copy of ctx carrying tx so that db.conn(ctx) returns tx
// instead of the pool. Callers MUST commit/rollback tx themselves; the context
// only routes the per-call executor.
func withTx(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, txCtxKey{}, tx)
}

// conn returns the executor for ctx: the transaction carried in ctx (set by
// withTx) if present, otherwise the pool. Methods that participate in the
// atomic rebuild call conn(ctx) instead of using db.pool directly, so the same
// method runs inside the rebuild's transaction when invoked from the build and
// against the pool when invoked from any other caller (admin UI, sync, etc.).
func (db *ResumeDB) conn(ctx context.Context) execConn {
	if tx, ok := ctx.Value(txCtxKey{}).(pgx.Tx); ok && tx != nil {
		return tx
	}
	return db.pool
}

// resumeMigrateLockKey is a distinct advisory-lock namespace for the resumedb
// migration runner. The gojob database is shared with hunt (pgutil's default
// lock key) and oversize (oversizeMigrateLockKey); each package-scoped runner
// MUST use a unique non-zero LockKey or concurrent boots would serialize
// unrelated migrations on the same advisory lock.
// ASCII "RESUM_MG" → 0x524553554D5F4D47.
const resumeMigrateLockKey int64 = 0x524553554D5F4D47

// runMigrations applies the embedded resume schema in lexical order via
// go-kit/pgutil.RunMigrations.
//
// pgutil owns soft-migration handling: a file whose first non-whitespace line
// is "-- soft" (002_resume_graph.sql for Apache AGE, 005_resume_vectors_embedding.sql
// for pgvector) may fail without aborting the run — the failure is logged and
// the file is NOT recorded as applied, so a later run retries it once the
// optional extension is installed. Non-soft failures abort as usual.
//
// No Baseline is set: the pre-pgutil runner had NO tracking table and re-applied
// every file on every startup, relying on IF NOT EXISTS / ADD COLUMN IF NOT
// EXISTS for idempotency. On the existing prod DB the first pgutil run applies
// all files in order — a no-op for the idempotent non-soft files, and a
// self-healing apply-or-soft-skip for 002/005 — then records them in
// resume_schema_migrations so subsequent boots skip them. A Baseline would
// instead mark the soft files applied-without-running and they would never
// retry, so it is intentionally omitted.
//
// pgutil runs each file in its own transaction on a single dedicated
// connection; a soft failure rolls back that file's tx (including any
// SET search_path it issued), so the ag_catalog contamination the old
// hand-rolled runner had to reset manually cannot persist across a soft
// failure. The non-soft files (003, 004_upwork, 006) additionally open with
// their own `SET search_path TO public`, matching the old behaviour.
func (db *ResumeDB) runMigrations(ctx context.Context) error {
	schemaSub, err := fs.Sub(schemaFS, "schema")
	if err != nil {
		return fmt.Errorf("resumedb: schema sub fs: %w", err)
	}
	return pgutil.RunMigrations(ctx, db.pool, schemaSub, pgutil.MigrateOptions{
		// Package-scoped tracking table — distinct from hunt's "schema_migrations"
		// and oversize's "oversize_schema_migrations", since all three share the
		// gojob DB.
		TableName: "resume_schema_migrations",
		LockKey:   resumeMigrateLockKey,
		// PreMigrate preserves the old behaviour: ensure search_path is public
		// before any DDL runs (the migrations use unqualified names, and the
		// gojob role has ag_catalog first in search_path).
		PreMigrate: func(ctx context.Context, conn *pgxpool.Conn) error {
			if _, err := conn.Exec(ctx, "SET search_path TO public"); err != nil {
				return fmt.Errorf("resumedb: set search_path: %w", err)
			}
			return nil
		},
	})
}

// accountClause is the ONE place that owns the account_id predicate for
// master/variant person statements (ADR-C(b)). It returns the SQL fragment
// (e.g. " AND account_id = $3") and the bind arg that scope a statement to an
// account. nil accountID = global (the expand-phase single-operator state); a
// non-nil accountID scopes the statement to that account.
//
// When the constrain phase flips account_id to NOT NULL, the nil branch is
// removed here and every caller that does not pass an account becomes a
// compile error — the predicate is load-bearing by construction, not by
// comment. startArg is the 1-based position of the account_id bind parameter
// in the final statement (after the caller's own args).
func accountClause(accountID *string, startArg int) (fragment string, arg any) {
	if accountID == nil {
		return "", nil
	}
	return fmt.Sprintf(" AND account_id = $%d", startArg), *accountID
}

// --- Person CRUD ---

type PersonRecord struct {
	ID              int               `json:"id"`
	Name            string            `json:"name"`
	Email           string            `json:"email"`
	Phone           string            `json:"phone"`
	Location        string            `json:"location"`
	Links           map[string]string `json:"links"`
	Summary         string            `json:"summary"`
	Headline        string            `json:"headline,omitempty"`
	HourlyRateCents int64             `json:"hourly_rate_cents,omitempty"`
	IsMaster        bool              `json:"is_master,omitempty"`
	ParentID        *int              `json:"parent_id,omitempty"`
	AccountID       *string           `json:"account_id,omitempty"`
}

func (db *ResumeDB) InsertPerson(ctx context.Context, p PersonRecord) (int, error) {
	linksJSON, _ := json.Marshal(p.Links)
	var id int
	err := db.conn(ctx).QueryRow(ctx,
		`INSERT INTO resume_persons (name, email, phone, location, links, summary, is_master, parent_id, account_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
		p.Name, p.Email, p.Phone, p.Location, linksJSON, p.Summary,
		p.IsMaster, p.ParentID, p.AccountID,
	).Scan(&id)
	if err != nil {
		return 0, err
	}
	// A new person changes what GetLatestPersonID returns — drop the craigslist
	// profile-location cache so the connector picks up the new latest person.
	invalidateProfileLocationCache()
	return id, nil
}

func (db *ResumeDB) ClearPerson(ctx context.Context, personID int) error {
	_, err := db.conn(ctx).Exec(ctx, `DELETE FROM resume_persons WHERE id = $1`, personID)
	if err != nil {
		return err
	}
	// Clearing a person changes what GetLatestPersonID returns — drop the
	// craigslist profile-location cache so the connector does not keep serving
	// a deleted person's location until the next successful rebuild.
	invalidateProfileLocationCache()
	return nil
}

// ClearAllPersons deletes all resume data (single-user system, rebuild from scratch).
// Deprecated: use ClearMasterPerson in new code — it preserves variants. Kept for
// BuildMasterResume's full-rebuild path which replaces the master entirely.
func (db *ResumeDB) ClearAllPersons(ctx context.Context) error {
	_, err := db.conn(ctx).Exec(ctx, `DELETE FROM resume_persons`)
	if err != nil {
		return err
	}
	invalidateProfileLocationCache()
	return nil
}

// captureVariantChildren returns the ids of variant rows whose parent_id equals
// masterID, BEFORE the master is deleted (the FK ON DELETE SET NULL would lose
// the link after the delete). Used by BuildMasterResume to capture an explicit
// list of children to re-parent to the new master — the re-parent then updates
// only those ids, not any unrelated parentless non-master row.
//
// accountID scopes the capture: nil = global (expand phase); non-nil = scoped.
// Runs inside the caller's transaction via conn(ctx).
func (db *ResumeDB) captureVariantChildren(ctx context.Context, masterID int, accountID *string) ([]int, error) {
	frag, arg := accountClause(accountID, 2)
	query := `SELECT id FROM resume_persons WHERE parent_id = $1` + frag
	var rows pgx.Rows
	var err error
	if arg == nil {
		rows, err = db.conn(ctx).Query(ctx, query, masterID)
	} else {
		rows, err = db.conn(ctx).Query(ctx, query, masterID, arg)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		if e := rows.Scan(&id); e != nil {
			return nil, e
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ClearMasterPerson deletes only the current master person (and cascaded data),
// preserving variants. Used by BuildMasterResume when rebuilding the master:
// the old master is removed, a new one is inserted with is_master=true.
// Variants (parent_id IS NOT NULL, is_master=false) are untouched.
//
// accountID scopes the delete: nil = global (expand-phase single-operator
// state); non-nil = scoped to that account. The predicate is owned by
// accountClause — when the constrain phase flips account_id NOT NULL, a
// missing account becomes a compile error, not a silent cross-account delete.
func (db *ResumeDB) ClearMasterPerson(ctx context.Context, accountID *string) error {
	frag, arg := accountClause(accountID, 1)
	if arg == nil {
		_, err := db.conn(ctx).Exec(ctx, `DELETE FROM resume_persons WHERE is_master = true`+frag)
		return err
	}
	_, err := db.conn(ctx).Exec(ctx, `DELETE FROM resume_persons WHERE is_master = true`+frag, arg)
	return err
}

// GetMasterPersonID returns the ID of the master person, or 0 if none exists.
// In the expand phase (account_id nullable) nil accountID returns the global
// master; a non-nil accountID scopes the lookup to that account. After the
// constrain phase, callers pass accountID to scope the lookup.
func (db *ResumeDB) GetMasterPersonID(ctx context.Context, accountID *string) int {
	frag, arg := accountClause(accountID, 1)
	query := `SELECT id FROM resume_persons WHERE is_master = true` + frag + ` ORDER BY id DESC LIMIT 1`
	var id int
	var err error
	if arg == nil {
		err = db.conn(ctx).QueryRow(ctx, query).Scan(&id)
	} else {
		err = db.conn(ctx).QueryRow(ctx, query, arg).Scan(&id)
	}
	if err != nil {
		return 0
	}
	return id
}

// GetMasterPersonIDChecked is the fail-closed variant for destructive surfaces.
// It distinguishes the states a destructive guard must tell apart:
//   - no master exists:    exists=false, id=0,   err=nil
//   - a master exists:     exists=true,  id=N,   err=nil
//   - multiple masters:    exists=false, id=0,   err!=nil   ← caller MUST refuse
//   - the query failed:    exists=false, id=0,   err!=nil   ← caller MUST refuse
//
// Multiple masters (more than one is_master=true row in scope) return an error
// rather than silently picking one — this value feeds the destructive-consent
// decision, and picking one of several is the same fail-open stance #366
// eliminated for the query-error path. Nothing prevents multiple masters until
// the constrain phase's unique partial index; until then this guard refuses.
//
// accountID scopes the lookup: nil = global (expand phase); non-nil = scoped.
func (db *ResumeDB) GetMasterPersonIDChecked(ctx context.Context, accountID *string) (exists bool, id int, err error) {
	frag, arg := accountClause(accountID, 1)
	query := `SELECT id FROM resume_persons WHERE is_master = true` + frag + ` ORDER BY id DESC LIMIT 2`
	var rows pgx.Rows
	if arg == nil {
		rows, err = db.conn(ctx).Query(ctx, query)
	} else {
		rows, err = db.conn(ctx).Query(ctx, query, arg)
	}
	if err != nil {
		return false, 0, err
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var i int
		if e := rows.Scan(&i); e != nil {
			return false, 0, e
		}
		ids = append(ids, i)
	}
	if e := rows.Err(); e != nil {
		return false, 0, e
	}
	if len(ids) == 0 {
		return false, 0, nil
	}
	if len(ids) > 1 {
		return false, 0, fmt.Errorf("GetMasterPersonIDChecked: multiple master persons found (ids=%v) — expected exactly one; refusing to pick one for a destructive decision", ids)
	}
	return true, ids[0], nil
}

// SetMasterPerson atomically promotes personID as the sole master, demoting any
// existing master. Transactional: the demote and promote either both apply or
// neither does, so there is never a window with zero or two masters.
//
// TODO(Phase 11 constrain): add accountID parameter and scope both UPDATEs with
// AND account_id = $accountID — currently demotes masters across ALL accounts
// (correct for expand phase: single global master).
func (db *ResumeDB) SetMasterPerson(ctx context.Context, personID int) error {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("SetMasterPerson: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `UPDATE resume_persons SET is_master = false WHERE is_master = true`); err != nil {
		return fmt.Errorf("SetMasterPerson: demote existing master: %w", err)
	}
	ct, err := tx.Exec(ctx, `UPDATE resume_persons SET is_master = true WHERE id = $1`, personID)
	if err != nil {
		return fmt.Errorf("SetMasterPerson: promote new master: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("SetMasterPerson: person %d not found", personID)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("SetMasterPerson: commit: %w", err)
	}
	return nil
}

// CreateVariant inserts a tailored variant derived from masterID. The variant
// carries is_master=false and parent_id=masterID. Returns the new variant's ID.
func (db *ResumeDB) CreateVariant(ctx context.Context, masterID int, p PersonRecord) (int, error) {
	linksJSON, _ := json.Marshal(p.Links)
	p.IsMaster = false
	p.ParentID = &masterID
	var id int
	err := db.conn(ctx).QueryRow(ctx,
		`INSERT INTO resume_persons (name, email, phone, location, links, summary, is_master, parent_id, account_id)
		 VALUES ($1, $2, $3, $4, $5, $6, false, $7, $8) RETURNING id`,
		p.Name, p.Email, p.Phone, p.Location, linksJSON, p.Summary, p.ParentID, p.AccountID,
	).Scan(&id)
	return id, err
}

// GetLatestPersonID returns the ID of the most recently created person, or 0 if none.
// It collapses "no rows" and "query failed" into the same 0 return, which is safe
// for read-only callers (no profile → nothing to read) but NOT for a destructive
// surface, where a transient pool error must not read as "no profile". Destructive
// callers use GetLatestPersonIDChecked instead.
//
// TODO(Phase 11 constrain): migrate all callers to GetMasterPersonID — with the
// master/variant model a variant can have a higher id than the master, so
// ORDER BY id DESC returns the wrong person. Callers that should use the master:
// resume_profile.go, resume_enrich.go, resume_gen.go, adminui/resume_edit.go,
// adminui/resume_handler.go, adminui/upwork.go, jobserver/tool_resume_profile.go.
// ALSO: internal/hunt/score/profile.go:164 runs its own inline
// `SELECT id FROM resume_persons ORDER BY id DESC LIMIT 1` (not a caller of
// this method) — it is invisible to a "migrate the callers" sweep and must be
// migrated to GetMasterPersonID in the same constrain-phase pass.
// Tracked in issue #388 constrain-phase migration.
func (db *ResumeDB) GetLatestPersonID(ctx context.Context) int {
	var id int
	err := db.conn(ctx).QueryRow(ctx, `SELECT id FROM resume_persons ORDER BY id DESC LIMIT 1`).Scan(&id)
	if err != nil {
		return 0
	}
	return id
}

// GetLatestPersonIDChecked is the fail-closed variant for destructive surfaces.
// It distinguishes the three states a destructive guard must tell apart:
//   - no profile exists:        exists=false, id=0,   err=nil
//   - a profile exists:         exists=true,  id=N,   err=nil
//   - the query failed:         exists=false, id=0,   err!=nil   ← caller MUST refuse
//
// On a destructive surface the zero value is the safe one: a caller that treats
// (exists=false) as "nothing to destroy" only destroys data when the query
// succeeded and genuinely found no rows, never when it failed.
func (db *ResumeDB) GetLatestPersonIDChecked(ctx context.Context) (exists bool, id int, err error) {
	err = db.conn(ctx).QueryRow(ctx, `SELECT id FROM resume_persons ORDER BY id DESC LIMIT 1`).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, 0, nil
		}
		return false, 0, err
	}
	return true, id, nil
}

// masterResumeRebuildLockKey is a distinct transaction-scoped advisory-lock
// namespace for BuildMasterResume's destructive rebuild. It is held for the
// duration of the rebuild transaction (pg_advisory_xact_lock, released on
// commit/rollback) so two concurrent rebuilds serialize: the second waits for
// the first to commit/rollback before it re-reads the person id under the lock,
// closing the TOCTOU where both read the same id pre-tx and both proceed.
// Distinct from resumeMigrateLockKey (session-scoped, migration runner only).
// ASCII "RSM_RBLD" → 0x52534D5F52424C44.
const masterResumeRebuildLockKey int64 = 0x52534D5F52424C44

// GetPerson returns the person record for the given ID.
func (db *ResumeDB) GetPerson(ctx context.Context, personID int) (*PersonRecord, error) {
	var p PersonRecord
	var linksJSON []byte
	err := db.pool.QueryRow(ctx,
		`SELECT id, name, COALESCE(email,''), COALESCE(phone,''), COALESCE(location,''), COALESCE(links,'{}'), COALESCE(summary,''),
		        COALESCE(headline,''), COALESCE(hourly_rate,0), is_master, parent_id, account_id
		 FROM resume_persons WHERE id = $1`, personID,
	).Scan(&p.ID, &p.Name, &p.Email, &p.Phone, &p.Location, &linksJSON, &p.Summary, &p.Headline, &p.HourlyRateCents,
		&p.IsMaster, &p.ParentID, &p.AccountID)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(linksJSON, &p.Links)
	return &p, nil
}

// GetPersonEnrichedAt returns the enriched_at timestamp as a string, or empty if not enriched.
func (db *ResumeDB) GetPersonEnrichedAt(ctx context.Context, personID int) string {
	var enrichedAt *string
	err := db.pool.QueryRow(ctx,
		`SELECT enriched_at::text FROM resume_persons WHERE id = $1`, personID,
	).Scan(&enrichedAt)
	if err != nil || enrichedAt == nil {
		return ""
	}
	return *enrichedAt
}
