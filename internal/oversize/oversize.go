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
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/anatolykoptev/go-kit/env"
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

// Migrate runs schema migrations in lexical order. Idempotent.
func (s *Store) Migrate(ctx context.Context) error {
	entries, err := schemaFS.ReadDir("schema")
	if err != nil {
		return fmt.Errorf("oversize: read schema dir: %w", err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("oversize: acquire migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SET search_path TO public"); err != nil {
		return fmt.Errorf("oversize: set search_path: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		data, err := schemaFS.ReadFile("schema/" + entry.Name())
		if err != nil {
			return fmt.Errorf("oversize: read %s: %w", entry.Name(), err)
		}
		if _, err := conn.Exec(ctx, string(data)); err != nil {
			return fmt.Errorf("oversize: execute %s: %w", entry.Name(), err)
		}
	}
	return nil
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
