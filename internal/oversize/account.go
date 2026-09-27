package oversize

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// AccountStore is the account-scoped view of Store (plan ADR-10). Every
// oversize row is owned by one panel_accounts.id; unscoped reads/writes are
// unreachable so a foreign account's spill is indistinguishable from absent.
type AccountStore struct {
	s   *Store
	aid uuid.UUID
}

// ForAccount binds the store to one account. uuid.Nil binds an unusable view:
// reads return empty/ErrNotFound and Save refuses — a Nil identity fails
// closed rather than writing into a global namespace.
func (s *Store) ForAccount(aid uuid.UUID) *AccountStore {
	return &AccountStore{s: s, aid: aid}
}

// AccountID returns the bound account.
func (a *AccountStore) AccountID() uuid.UUID { return a.aid }

// errNoAccountScope is returned by writes on a Nil-account view.
var errNoAccountScope = errors.New("oversize: account scope required (uuid.Nil is not a writable account)")

// entryCols is the shared SELECT column list for scoped reads.
const entryCols = `id, tool_name, query_hash, payload, size_bytes, sha256, sample, item_count, created_at`

// Save inserts an entry owned by the bound account, returns generated id.
func (a *AccountStore) Save(ctx context.Context, e Entry) (int64, error) {
	if a.aid == uuid.Nil {
		return 0, errNoAccountScope
	}
	var id int64
	err := a.s.pool.QueryRow(ctx, `
		INSERT INTO oversize_responses
			(tool_name, query_hash, payload, size_bytes, sha256, sample, item_count, account_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id`,
		e.ToolName,
		e.QueryHash,
		[]byte(e.Payload),
		e.SizeBytes,
		e.SHA256,
		nullableJSON(e.Sample),
		e.ItemCount,
		a.aid,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("oversize: save: %w", err)
	}
	return id, nil
}

// Get returns the bound account's entry by id; ErrNotFound if missing,
// soft-deleted, or owned by another account — no distinguishable leak.
func (a *AccountStore) Get(ctx context.Context, id int64) (*Entry, error) {
	row := a.s.pool.QueryRow(ctx, `
		SELECT `+entryCols+`
		FROM oversize_responses
		WHERE id = $1 AND account_id = $2 AND deleted_at IS NULL`, id, a.aid)

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
	e.AccountID = a.aid
	if len(sample) > 0 {
		e.Sample = json.RawMessage(sample)
	}
	return &e, nil
}

// List returns the bound account's recent entries newest-first.
func (a *AccountStore) List(ctx context.Context, f ListFilter) ([]Entry, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}

	// $1 is always the account scope; optional filters shift to $2/$3.
	where := "account_id = $1 AND deleted_at IS NULL"
	args := []any{a.aid}
	pos := 2
	if f.ToolName != "" {
		where += fmt.Sprintf(" AND tool_name = $%d", pos)
		args = append(args, f.ToolName)
		pos++
	}
	if !f.Since.IsZero() {
		where += fmt.Sprintf(" AND created_at > $%d", pos)
		args = append(args, f.Since)
		pos++
	}
	args = append(args, limit)

	rows, err := a.s.pool.Query(ctx, `
		SELECT `+entryCols+`
		FROM oversize_responses
		WHERE `+where+`
		ORDER BY created_at DESC
		LIMIT $`+strconv.Itoa(pos), args...)
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
		e.AccountID = a.aid
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

// Purge soft-deletes only the bound account's entries older than `before`.
// Fleet-wide retention stays on Store.Purge (auto-purge job).
func (a *AccountStore) Purge(ctx context.Context, before time.Time) (int64, error) {
	tag, err := a.s.pool.Exec(ctx,
		`UPDATE oversize_responses SET deleted_at = NOW()
		 WHERE created_at < $1 AND account_id = $2 AND deleted_at IS NULL`,
		before, a.aid)
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
