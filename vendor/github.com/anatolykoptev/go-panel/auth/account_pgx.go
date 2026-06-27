package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PgxAccountStore implements AccountStore over a pgxpool.Pool. It is the
// framework-provided store for multi-user operator auth: a consumer passes its
// pool and calls EnsureSchema once at boot.
type PgxAccountStore struct {
	pool *pgxpool.Pool
}

// NewPgxAccountStore wraps pool as an AccountStore.
func NewPgxAccountStore(pool *pgxpool.Pool) *PgxAccountStore {
	return &PgxAccountStore{pool: pool}
}

var _ AccountStore = (*PgxAccountStore)(nil)

// accountSchemaSQL is the idempotent DDL for the framework-owned accounts table.
// The TOTP columns are present from the start (nullable / default false) so the
// 2FA feature needs no later schema change.
const accountSchemaSQL = `
CREATE TABLE IF NOT EXISTS panel_accounts (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email         TEXT NOT NULL,
    name          TEXT NOT NULL DEFAULT '',
    password_hash TEXT,
    role          TEXT NOT NULL DEFAULT 'admin',
    active        BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_login_at TIMESTAMPTZ,
    totp_secret      TEXT,
    totp_enabled     BOOLEAN NOT NULL DEFAULT false,
    totp_enrolled_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_panel_accounts_email ON panel_accounts(email);`

// EnsureSchema creates the accounts table and indexes if they do not exist.
// Idempotent; safe to call on every boot.
func (s *PgxAccountStore) EnsureSchema(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, accountSchemaSQL); err != nil {
		return fmt.Errorf("auth: ensure account schema: %w", err)
	}
	return nil
}

const selectAccountByEmailSQL = `
SELECT id, email, name, password_hash, role, active, totp_enabled
FROM panel_accounts
WHERE email = $1 AND active = true AND password_hash IS NOT NULL`

// GetByEmail implements AccountStore.
func (s *PgxAccountStore) GetByEmail(ctx context.Context, email string) (*Account, error) {
	var a Account
	err := s.pool.QueryRow(ctx, selectAccountByEmailSQL, email).
		Scan(&a.ID, &a.Email, &a.Name, &a.PasswordHash, &a.Role, &a.Active, &a.TOTPEnabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAccountNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("auth: get account by email: %w", err)
	}
	return &a, nil
}

const selectAccountByIDSQL = `
SELECT id, email, name, role, active, totp_enabled
FROM panel_accounts WHERE id = $1`

// GetByID implements AccountStore. It does not load the password hash.
func (s *PgxAccountStore) GetByID(ctx context.Context, id string) (*Account, error) {
	var a Account
	err := s.pool.QueryRow(ctx, selectAccountByIDSQL, id).
		Scan(&a.ID, &a.Email, &a.Name, &a.Role, &a.Active, &a.TOTPEnabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAccountNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("auth: get account by id: %w", err)
	}
	return &a, nil
}

// UpdateLastLogin implements AccountStore.
func (s *PgxAccountStore) UpdateLastLogin(ctx context.Context, id string) error {
	if _, err := s.pool.Exec(ctx, "UPDATE panel_accounts SET last_login_at = now() WHERE id = $1", id); err != nil {
		return fmt.Errorf("auth: update last login: %w", err)
	}
	return nil
}

// UpdatePasswordHash implements AccountStore.
func (s *PgxAccountStore) UpdatePasswordHash(ctx context.Context, id, passwordHash string) error {
	ct, err := s.pool.Exec(ctx, "UPDATE panel_accounts SET password_hash = $1 WHERE id = $2", passwordHash, id)
	if err != nil {
		return fmt.Errorf("auth: update password hash: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrAccountNotFound
	}
	return nil
}

const insertAccountSQL = `
INSERT INTO panel_accounts (email, name, password_hash, role, active)
VALUES ($1, $2, $3, $4, true)
ON CONFLICT (email) DO NOTHING
RETURNING id`

// CreateAccount implements AccountStore. Idempotent on email conflict.
func (s *PgxAccountStore) CreateAccount(ctx context.Context, email, name, passwordHash, role string) (string, bool, error) {
	var id string
	err := s.pool.QueryRow(ctx, insertAccountSQL, email, name, passwordHash, role).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil // account with this email already exists
	}
	if err != nil {
		return "", false, fmt.Errorf("auth: create account: %w", err)
	}
	return id, true, nil
}
