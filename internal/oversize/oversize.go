// Package oversize stores MCP tool responses that exceed the MCP envelope
// size limit (~25KB). Callers should wrap their output via spill.MaybeSpill;
// if the payload is too large to inline, it is persisted here and the caller
// returns a small envelope pointing to the stored record.
package oversize

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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema/*.sql
var schemaFS embed.FS

// ErrNotFound is returned by Get when no row matches.
var ErrNotFound = errors.New("oversize: entry not found")

// OBS-6: purge metric hooks — set by engine.Init via SetPurgeMetricHooks.
// Avoids import cycle (engine imports oversize via oversize_singleton).
var (
	onPurgeDeleted func(int64)
	onPurgeError   func()
)

// SetPurgeMetricHooks wires the purge metric callbacks. Called from engine.Init.
func SetPurgeMetricHooks(deletedFn func(int64), errorFn func()) {
	onPurgeDeleted = deletedFn
	onPurgeError = errorFn
}

// Entry is a stored oversize response.
type Entry struct {
	ID        int64           `json:"id"`
	ToolName  string          `json:"tool_name"`
	QueryHash string          `json:"query_hash,omitempty"`
	Payload   json.RawMessage `json:"payload"`
	SizeBytes int             `json:"size_bytes"`
	SHA256    string          `json:"sha256"`
	Sample    json.RawMessage `json:"sample,omitempty"`
	ItemCount int             `json:"item_count"`
	CreatedAt time.Time       `json:"created_at"`
}

// Store is the Postgres-backed oversize store.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore returns a Store using the given pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// oversizeMigrateLockKey is a distinct advisory-lock namespace for the oversize
// migration runner. The gojob database is shared with hunt (which owns pgutil's
// default lock key) and resumedb; each package-scoped runner MUST use a unique
// non-zero LockKey or concurrent boots would deadlock on the same advisory lock.
// ASCII "OVRSZ_MG" → 0x4F5652535A5F4D47.
const oversizeMigrateLockKey int64 = 0x4F5652535A5F4D47

// Migrate runs schema migrations in lexical order via go-kit/pgutil.
// Idempotent: already-applied files are skipped; on an empty tracking table
// the Baseline predicate adopts an existing prod DB (marks all files applied
// without re-running them) so the cutover from the old no-tracker runner —
// which re-applied every file on every startup — never re-runs DDL against
// the live schema.
//
// pgutil owns its own `oversize_schema_migrations(name, checksum, applied_at)`
// table; the pre-pgutil runner had NO tracking table (it re-ran both files
// every boot, relying on IF NOT EXISTS / ADD COLUMN IF NOT EXISTS for
// idempotency). Baseline adopts by probing for the app table
// (oversize_responses): if it exists, every current DB was brought up by that
// re-apply-every-boot runner, so all files have already been applied — marking
// them applied without executing avoids a pointless DDL re-run.
func (s *Store) Migrate(ctx context.Context) error {
	schemaSub, err := fs.Sub(schemaFS, "schema")
	if err != nil {
		return fmt.Errorf("oversize: schema sub fs: %w", err)
	}
	return pgutil.RunMigrations(ctx, s.pool, schemaSub, pgutil.MigrateOptions{
		// Package-scoped tracking table — distinct from hunt's "schema_migrations"
		// and any future resumedb table, since all three share the gojob DB.
		TableName: "oversize_schema_migrations",
		LockKey:   oversizeMigrateLockKey,
		// PreMigrate preserves the old behaviour: ensure search_path is public
		// before any DDL runs (the migrations use unqualified names).
		PreMigrate: func(ctx context.Context, conn *pgxpool.Conn) error {
			if _, err := conn.Exec(ctx, "SET search_path TO public"); err != nil {
				return fmt.Errorf("oversize: set search_path: %w", err)
			}
			return nil
		},
		// Baseline: adopt an already-migrated prod DB. The pre-pgutil runner
		// had no tracking table and re-applied every file on every startup, so
		// any DB where oversize_responses exists has already had ALL current
		// files applied. Return true → pgutil marks every file applied without
		// executing, avoiding a pointless DDL re-run against the live schema.
		// On a fresh DB to_regclass('public.oversize_responses') IS NULL →
		// false → pgutil applies all files in order.
		//
		// CUTOVER SAFETY (pr-council #309, finding 3): do NOT add a NEW
		// schema/*.sql file in the same PR that first deploys pgutil to a DB
		// whose tables already exist. Baseline marks ALL discovered files
		// applied WITHOUT running their DDL, so a brand-new file would be
		// silently skipped. New migrations must land in a LATER PR, after
		// oversize_schema_migrations is already populated.
		Baseline: func(ctx context.Context, conn *pgxpool.Conn) (bool, error) {
			var exists bool
			if err := conn.QueryRow(ctx,
				`SELECT to_regclass('public.oversize_responses') IS NOT NULL`,
			).Scan(&exists); err != nil {
				return false, fmt.Errorf("oversize: baseline probe: %w", err)
			}
			return exists, nil
		},
	})
}

// Save inserts an entry, returns generated id.
func (s *Store) Save(ctx context.Context, e Entry) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO oversize_responses
			(tool_name, query_hash, payload, size_bytes, sha256, sample, item_count)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id`,
		e.ToolName,
		e.QueryHash,
		[]byte(e.Payload),
		e.SizeBytes,
		e.SHA256,
		nullableJSON(e.Sample),
		e.ItemCount,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("oversize: save: %w", err)
	}
	return id, nil
}

// Get returns entry by id; ErrNotFound if missing or soft-deleted.
// BH-9: filters deleted_at IS NULL so purged entries are invisible to
// concurrent reads even before the hard purge removes the row.
func (s *Store) Get(ctx context.Context, id int64) (*Entry, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, tool_name, query_hash, payload, size_bytes, sha256, sample, item_count, created_at
		FROM oversize_responses
		WHERE id = $1 AND deleted_at IS NULL`, id)

	var e Entry
	var sample []byte
	err := row.Scan(
		&e.ID, &e.ToolName, &e.QueryHash,
		&e.Payload, &e.SizeBytes, &e.SHA256,
		&sample, &e.ItemCount, &e.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("oversize: get: %w", err)
	}
	if len(sample) > 0 {
		e.Sample = json.RawMessage(sample)
	}
	return &e, nil
}

// ListFilter narrows List results.
type ListFilter struct {
	ToolName string
	Since    time.Time
	Limit    int // default 20, max 200
}

// List returns recent entries newest-first.
func (s *Store) List(ctx context.Context, f ListFilter) ([]Entry, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}

	var (
		rows pgx.Rows
		err  error
	)

	hasTool := f.ToolName != ""
	hasSince := !f.Since.IsZero()
	switch {
	case hasTool && hasSince:
		rows, err = s.pool.Query(ctx, `
			SELECT id, tool_name, query_hash, payload, size_bytes, sha256, sample, item_count, created_at
			FROM oversize_responses
			WHERE tool_name = $1 AND created_at > $2 AND deleted_at IS NULL
			ORDER BY created_at DESC
			LIMIT $3`, f.ToolName, f.Since, limit)
	case hasTool:
		rows, err = s.pool.Query(ctx, `
			SELECT id, tool_name, query_hash, payload, size_bytes, sha256, sample, item_count, created_at
			FROM oversize_responses
			WHERE tool_name = $1 AND deleted_at IS NULL
			ORDER BY created_at DESC
			LIMIT $2`, f.ToolName, limit)
	case hasSince:
		rows, err = s.pool.Query(ctx, `
			SELECT id, tool_name, query_hash, payload, size_bytes, sha256, sample, item_count, created_at
			FROM oversize_responses
			WHERE created_at > $1 AND deleted_at IS NULL
			ORDER BY created_at DESC
			LIMIT $2`, f.Since, limit)
	default:
		rows, err = s.pool.Query(ctx, `
			SELECT id, tool_name, query_hash, payload, size_bytes, sha256, sample, item_count, created_at
			FROM oversize_responses
			WHERE deleted_at IS NULL
			ORDER BY created_at DESC
			LIMIT $1`, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("oversize: list query: %w", err)
	}
	defer rows.Close()

	var result []Entry
	for rows.Next() {
		var e Entry
		var sample []byte
		if err := rows.Scan(
			&e.ID, &e.ToolName, &e.QueryHash,
			&e.Payload, &e.SizeBytes, &e.SHA256,
			&sample, &e.ItemCount, &e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("oversize: list scan: %w", err)
		}
		if len(sample) > 0 {
			e.Sample = json.RawMessage(sample)
		}
		result = append(result, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("oversize: list rows: %w", err)
	}
	return result, nil
}

// Purge soft-deletes entries older than `before` by setting deleted_at=NOW().
// BH-9: soft delete prevents races with concurrent Get/List reads — a
// hard DELETE can execute before or during a read, causing ErrNotFound.
// HardPurge removes rows with deleted_at older than 24h.
func (s *Store) Purge(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE oversize_responses SET deleted_at = NOW() WHERE created_at < $1 AND deleted_at IS NULL`, before)
	if err != nil {
		if onPurgeError != nil {
			onPurgeError()
		}
		return 0, fmt.Errorf("oversize: purge: %w", err)
	}
	n := tag.RowsAffected()
	if onPurgeDeleted != nil {
		onPurgeDeleted(n)
	}
	return n, nil
}

// HardPurge permanently deletes rows soft-deleted more than 24h ago.
// BH-9: second-stage hard purge that removes rows after the soft-delete
// grace period, keeping the table bounded without racing with active reads.
func (s *Store) HardPurge(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM oversize_responses WHERE deleted_at IS NOT NULL AND deleted_at < NOW() - INTERVAL '24 hours'`)
	if err != nil {
		if onPurgeError != nil {
			onPurgeError()
		}
		return 0, fmt.Errorf("oversize: hard purge: %w", err)
	}
	return tag.RowsAffected(), nil
}

// StartAutoPurge runs a background goroutine that purges entries older than
// the retention period every purgeInterval. #185 fix: prevents unbounded
// table growth. Default retention = 7 days, purge interval = 1h.
// Both are env-overridable via OVERSIZE_RETENTION and OVERSIZE_PURGE_INTERVAL.
func (s *Store) StartAutoPurge(ctx context.Context) {
	if s == nil {
		return
	}
	retention := env.MustDuration("OVERSIZE_RETENTION", 7*24*time.Hour)
	interval := env.MustDuration("OVERSIZE_PURGE_INTERVAL", time.Hour)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				before := time.Now().Add(-retention)
				deleted, err := s.Purge(ctx, before)
				if err != nil {
					slog.Warn("oversize: auto-purge failed", slog.Any("error", err))
					continue
				}
				// BH-9: hard purge rows soft-deleted >24h ago.
				hardDeleted, err := s.HardPurge(ctx)
				if err != nil {
					slog.Warn("oversize: hard purge failed", slog.Any("error", err))
				}
				if deleted > 0 || hardDeleted > 0 {
					slog.Info("oversize: auto-purge complete",
						slog.Int64("soft_deleted", deleted),
						slog.Int64("hard_deleted", hardDeleted),
						slog.Duration("retention", retention))
				}
			}
		}
	}()
}

// nullableJSON returns nil when the raw message is empty, otherwise returns
// the byte slice so pgx stores it as JSONB NULL vs a real value.
func nullableJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}
