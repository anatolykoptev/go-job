// admin.go — the gojob-admin CLI seam surface (plan ADR-12). Everything here
// is additive: Bootstrap and the P0/P1 store API stay untouched, while the CLI
// gets real store methods instead of raw SQL (the mcpkeys.go contract).
//
// Why these helpers exist instead of reusing vendored go-panel methods:
//
//   - auth.PgxAccountStore.CreateAccount takes a non-nullable passwordHash
//     string; a ” value satisfies GetByEmail's `password_hash IS NOT NULL`
//     filter, so a key-only account would present as a (fail-closed) password
//     login candidate. CreateAccount here takes *string so key-only accounts
//     store a real NULL.
//   - PgxAccountStore.ListAccounts deliberately omits created_at; the CLI's
//     account list output needs it.
//   - PgxAccountStore.GetByEmail filters `active = true AND password_hash IS
//     NOT NULL` — right for login, wrong for ops: deactivate/mint must find
//     inactive and passwordless accounts too. ResolveAccountID has no filter.
//   - account_hunt_settings lands in P3; SetNotifyChatID probes for the table
//     so the --notify-chat-id flag is usable today without inventing the DDL.
package accounts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anatolykoptev/go-panel/auth"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CreateAccount inserts a panel_accounts row with an explicitly NULL-able
// password hash (nil hash → NULL: a key-only account must not satisfy
// GetByEmail's password_hash IS NOT NULL filter). Email conflict returns
// created=false with the existing row's id — idempotent, matching the
// framework CreateAccount contract.
//
// The role argument is NOT validated here — the CLI validates against
// {user,admin} before calling and the panel_accounts_role_check CHECK
// constraint (Bootstrap) makes anything else unwritable at the DB layer.
func CreateAccount(ctx context.Context, pool *pgxpool.Pool, email, name string, passwordHash *string, role string) (uuid.UUID, bool, error) {
	var id uuid.UUID
	err := pool.QueryRow(ctx, `
		INSERT INTO panel_accounts (email, name, password_hash, role, active)
		VALUES ($1, $2, $3, $4, true)
		ON CONFLICT (email) DO NOTHING
		RETURNING id`,
		email, name, passwordHash, role).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		// Email taken: return the existing row's id (like seedOperator's
		// conflict path) so callers can still act on the account.
		if err := pool.QueryRow(ctx,
			`SELECT id FROM panel_accounts WHERE email = $1`, email).Scan(&id); err != nil {
			return uuid.Nil, false, fmt.Errorf("accounts: resolve existing account: %w", err)
		}
		return id, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("accounts: create account: %w", err)
	}
	return id, true, nil
}

// AccountRow is the operator-facing projection of panel_accounts for
// `account list` — the framework ListAccounts shape plus created_at, and like
// it, never selects password_hash.
type AccountRow struct {
	ID        uuid.UUID
	Email     string
	Name      string
	Role      string
	Active    bool
	CreatedAt time.Time
}

// ListAccountRows returns every account — active and inactive — oldest first.
func ListAccountRows(ctx context.Context, pool *pgxpool.Pool) ([]AccountRow, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, email, name, role, active, created_at
		FROM panel_accounts ORDER BY created_at, email`)
	if err != nil {
		return nil, fmt.Errorf("accounts: list: %w", err)
	}
	defer rows.Close()

	var out []AccountRow
	for rows.Next() {
		var r AccountRow
		if err := rows.Scan(&r.ID, &r.Email, &r.Name, &r.Role, &r.Active, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("accounts: list scan: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("accounts: list rows: %w", err)
	}
	return out, nil
}

// ResolveAccountID maps a CLI --account/--id/--email reference to a
// panel_accounts id. A parseable UUID resolves by id, anything else by email.
// Unlike auth.PgxAccountStore.GetByEmail there is NO active/password filter —
// operations must reach deactivated and key-only accounts. Missing rows map
// to auth.ErrAccountNotFound.
func ResolveAccountID(ctx context.Context, pool *pgxpool.Pool, ref string) (uuid.UUID, error) {
	var id uuid.UUID
	var err error
	if parsed, perr := uuid.Parse(ref); perr == nil {
		err = pool.QueryRow(ctx,
			`SELECT id FROM panel_accounts WHERE id = $1`, parsed).Scan(&id)
	} else {
		err = pool.QueryRow(ctx,
			`SELECT id FROM panel_accounts WHERE email = $1`, ref).Scan(&id)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, auth.ErrAccountNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("accounts: resolve %q: %w", ref, err)
	}
	return id, nil
}

// SetNotifyChatID stores the account's Telegram chat id in
// account_hunt_settings — IF that table exists. It is created by a later task
// (P3); when absent the call returns stored=false so the CLI can warn instead
// of failing. The written row carries enabled=false per ADR-12 (provisioning
// never silently arms hunts).
//
// The INSERT column set (account_id, notify_chat_id, enabled) follows the
// ADR-12 contract; if P3 ships a different shape this single statement is the
// adjustment point.
func SetNotifyChatID(ctx context.Context, pool *pgxpool.Pool, accountID uuid.UUID, chatID int64) (stored bool, err error) {
	var reg *string
	if err := pool.QueryRow(ctx,
		`SELECT to_regclass('account_hunt_settings')::text`).Scan(&reg); err != nil {
		return false, fmt.Errorf("accounts: probe account_hunt_settings: %w", err)
	}
	if reg == nil {
		return false, nil
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO account_hunt_settings (account_id, notify_chat_id, enabled)
		VALUES ($1, $2, false)
		ON CONFLICT (account_id) DO UPDATE SET notify_chat_id = EXCLUDED.notify_chat_id`,
		accountID, chatID); err != nil {
		return false, fmt.Errorf("accounts: set notify_chat_id: %w", err)
	}
	return true, nil
}

// KeyInfo is the listable projection of an mcp_api_keys row — everything an
// operator needs to identify a key. key_hash is never selected: a listing must
// not lift credential material (same rule as ListAccounts and password_hash).
type KeyInfo struct {
	ID         uuid.UUID
	Prefix     string
	Label      string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// ListKeys returns mcp_api_keys rows oldest-first — for accountID uuid.Nil all
// accounts' keys, otherwise just that account's. Revoked rows are included so
// the operator can audit (revoked_at shows the state).
func (k *KeyStore) ListKeys(ctx context.Context, accountID uuid.UUID) ([]KeyInfo, error) {
	query := `
		SELECT id, key_prefix, label, created_at, last_used_at, revoked_at
		FROM mcp_api_keys`
	args := []any{}
	if accountID != uuid.Nil {
		query += ` WHERE account_id = $1`
		args = append(args, accountID)
	}
	query += ` ORDER BY created_at`

	rows, err := k.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("mcp key list: %w", err)
	}
	defer rows.Close()

	var out []KeyInfo
	for rows.Next() {
		var ki KeyInfo
		if err := rows.Scan(&ki.ID, &ki.Prefix, &ki.Label, &ki.CreatedAt, &ki.LastUsedAt, &ki.RevokedAt); err != nil {
			return nil, fmt.Errorf("mcp key list scan: %w", err)
		}
		out = append(out, ki)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mcp key list rows: %w", err)
	}
	return out, nil
}

// ActiveKeyIDsByPrefix returns the ids of ACTIVE (unrevoked) keys whose
// key_prefix equals prefix. The CLI's revoke-by-prefix resolves through this:
// 0 matches → not found, exactly 1 → safe to Revoke, more → ambiguous and the
// caller must refuse rather than guess.
func (k *KeyStore) ActiveKeyIDsByPrefix(ctx context.Context, prefix string) ([]uuid.UUID, error) {
	rows, err := k.pool.Query(ctx,
		`SELECT id FROM mcp_api_keys WHERE key_prefix = $1 AND revoked_at IS NULL`, prefix)
	if err != nil {
		return nil, fmt.Errorf("mcp key prefix lookup: %w", err)
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("mcp key prefix scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mcp key prefix rows: %w", err)
	}
	return ids, nil
}

// RegisterPending inserts a panel_accounts row for the PUBLIC self-serve
// registration surface (P6). role and active are SQL LITERALS, never
// parameters — the unauthenticated endpoint can never mint an admin row or
// an already-active one; the panel_accounts_role_check CHECK constraint is
// the second wall.
//
// Email conflict returns created=false: ON CONFLICT (email) DO NOTHING
// leaves the existing row byte-for-byte untouched (no oracle: the caller
// renders an identical success page either way). The caller hashes the
// password BEFORE calling us regardless of conflict, so the bcrypt cost
// equalizes the taken/untaken timing.
//
// RegisterPending is the ONLY panel_accounts write the anonymous surface
// performs — deliberate divergence from CreateAccount (active=true, role
// parameterized) which stays the trusted CLI seam.
func RegisterPending(ctx context.Context, pool *pgxpool.Pool, email, name, passwordHash string) (created bool, err error) {
	ct, err := pool.Exec(ctx, `
		INSERT INTO panel_accounts (email, name, password_hash, role, active)
		VALUES ($1, $2, $3, 'user', false)
		ON CONFLICT (email) DO NOTHING`,
		email, name, passwordHash)
	if err != nil {
		return false, fmt.Errorf("accounts: register pending: %w", err)
	}
	return ct.RowsAffected() == 1, nil
}

// PendingLoginHint returns the auth.BcryptConfig.LoginFailHint hook for the
// bcrypt driver: when a login fails on the unknown/inactive-email branch
// (go-panel runs its timing-equalizing dummy compare BEFORE consulting the
// hook, so this is never a timing oracle), it looks up the email WITHOUT
// GetByEmail's active/password filters and reports one narrow fact — "a
// passworded registration exists but is not yet active":
//
//   - row exists AND active=false AND password_hash IS NOT NULL
//     → ("This account is registered but not yet active — an operator will
//     approve it.", true)
//   - every other shape (no row, active row mid-flight, inactive
//     passwordless/key-only account — such an account can never log in
//     anyway) → ("", false): the generic "Invalid email or password" stands.
//
// Enumeration tradeoff (accepted in the P6 plan): the hint reveals
// pending/inactive-passworded registration status behind the same 5/min/IP
// login throttle. It never distinguishes an active account from a
// nonexistent one.
//
// The hook receives the email go-panel already normalized
// (verifyPassword lower-trims the form value before GetByEmail) and
// normalizes identically itself — defense in depth for any future caller.
func PendingLoginHint(pool *pgxpool.Pool) func(ctx context.Context, email string) (string, bool) {
	return func(ctx context.Context, email string) (string, bool) {
		email = strings.ToLower(strings.TrimSpace(email))
		var active, hasPassword bool
		err := pool.QueryRow(ctx,
			`SELECT active, password_hash IS NOT NULL FROM panel_accounts WHERE email = $1`,
			email).Scan(&active, &hasPassword)
		if err != nil {
			// ErrNoRows (no such email) AND transient errors alike keep the
			// generic message — a DB hiccup must not surface as a login hint
			// or an extra 5xx, the request already failed closed as 401.
			return "", false
		}
		if active || !hasPassword {
			return "", false
		}
		return "This account is registered but not yet active — an operator will approve it.", true
	}
}
