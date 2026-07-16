package auth

import (
	"context"
	"encoding/base64"
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
var _ TOTPStore = (*PgxAccountStore)(nil)

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

// totpSchemaSQL is the additive, idempotent DDL for TOTPStore: the
// replay-guard column on the existing accounts table, plus the recovery
// codes child table. Kept as its own constant/Exec (rather than folded
// into accountSchemaSQL) so the P3 TOTP addition is a self-contained diff
// against the P0-shipped accounts schema.
const totpSchemaSQL = `
ALTER TABLE panel_accounts ADD COLUMN IF NOT EXISTS totp_last_step BIGINT;

CREATE TABLE IF NOT EXISTS panel_totp_recovery_codes (
    id         BIGSERIAL PRIMARY KEY,
    account_id UUID NOT NULL REFERENCES panel_accounts(id) ON DELETE CASCADE,
    code_hash  BYTEA NOT NULL,
    used_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_panel_totp_recovery_codes_account_hash UNIQUE (account_id, code_hash)
);
CREATE INDEX IF NOT EXISTS idx_panel_totp_recovery_codes_account ON panel_totp_recovery_codes(account_id);`

// EnsureSchema creates the accounts table, the TOTP columns/tables, and
// indexes if they do not exist. Idempotent; safe to call on every boot.
func (s *PgxAccountStore) EnsureSchema(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, accountSchemaSQL); err != nil {
		return fmt.Errorf("auth: ensure account schema: %w", err)
	}
	if _, err := s.pool.Exec(ctx, totpSchemaSQL); err != nil {
		return fmt.Errorf("auth: ensure totp schema: %w", err)
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

// SetPendingTOTPSecret implements TOTPStore. Resets totp_enabled and
// totp_enrolled_at so a re-enrollment attempt can never leave the row
// claiming a confirmed 2FA state for a secret that was never verified.
func (s *PgxAccountStore) SetPendingTOTPSecret(ctx context.Context, accountID string, encryptedSecret []byte) error {
	encoded := base64.StdEncoding.EncodeToString(encryptedSecret)
	ct, err := s.pool.Exec(ctx, `
UPDATE panel_accounts
SET totp_secret = $1, totp_enabled = false, totp_enrolled_at = NULL
WHERE id = $2`, encoded, accountID)
	if err != nil {
		return fmt.Errorf("auth: set pending totp secret: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrAccountNotFound
	}
	return nil
}

// ConfirmTOTPEnrollment implements TOTPStore.
func (s *PgxAccountStore) ConfirmTOTPEnrollment(ctx context.Context, accountID string) error {
	ct, err := s.pool.Exec(ctx, `
UPDATE panel_accounts SET totp_enabled = true, totp_enrolled_at = now() WHERE id = $1`, accountID)
	if err != nil {
		return fmt.Errorf("auth: confirm totp enrollment: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrAccountNotFound
	}
	return nil
}

// ConfirmTOTPEnrollmentWithRecoveryCodes implements TOTPStore atomically:
// the enrollment is confirmed and the recovery codes are replaced in one
// transaction, so an enabled account can never be left without codes.
func (s *PgxAccountStore) ConfirmTOTPEnrollmentWithRecoveryCodes(ctx context.Context, accountID string, hashedCodes [][]byte) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("auth: confirm totp enrollment with recovery codes: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	ct, err := tx.Exec(ctx, `
UPDATE panel_accounts SET totp_enabled = true, totp_enrolled_at = now() WHERE id = $1`, accountID)
	if err != nil {
		return fmt.Errorf("auth: confirm totp enrollment with recovery codes: update account: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrAccountNotFound
	}

	if _, err := tx.Exec(ctx, `DELETE FROM panel_totp_recovery_codes WHERE account_id = $1`, accountID); err != nil {
		return fmt.Errorf("auth: confirm totp enrollment with recovery codes: clear existing: %w", err)
	}
	for _, h := range hashedCodes {
		if _, err := tx.Exec(ctx, `
INSERT INTO panel_totp_recovery_codes (account_id, code_hash) VALUES ($1, $2)`, accountID, h); err != nil {
			return fmt.Errorf("auth: confirm totp enrollment with recovery codes: insert: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("auth: confirm totp enrollment with recovery codes: commit: %w", err)
	}
	return nil
}

// GetTOTPSecret implements TOTPStore.
func (s *PgxAccountStore) GetTOTPSecret(ctx context.Context, accountID string) ([]byte, error) {
	var encoded *string
	err := s.pool.QueryRow(ctx, `SELECT totp_secret FROM panel_accounts WHERE id = $1`, accountID).Scan(&encoded)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAccountNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("auth: get totp secret: %w", err)
	}
	if encoded == nil {
		return nil, ErrTOTPNotEnrolled
	}
	raw, err := base64.StdEncoding.DecodeString(*encoded)
	if err != nil {
		return nil, fmt.Errorf("auth: decode totp secret: %w", err)
	}
	return raw, nil
}

// DisableTOTP implements TOTPStore. Transactional: the account row reset
// and the recovery-code purge either both apply or neither does, so TOTP
// disable can never leave orphaned recovery codes usable against a
// "disabled" account.
func (s *PgxAccountStore) DisableTOTP(ctx context.Context, accountID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("auth: disable totp: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	ct, err := tx.Exec(ctx, `
UPDATE panel_accounts
SET totp_secret = NULL, totp_enabled = false, totp_enrolled_at = NULL, totp_last_step = NULL
WHERE id = $1`, accountID)
	if err != nil {
		return fmt.Errorf("auth: disable totp: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrAccountNotFound
	}
	if _, err := tx.Exec(ctx, `DELETE FROM panel_totp_recovery_codes WHERE account_id = $1`, accountID); err != nil {
		return fmt.Errorf("auth: disable totp: clear recovery codes: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("auth: disable totp: commit: %w", err)
	}
	return nil
}

// ConsumeTOTPStep implements TOTPStore: an atomic check-and-advance. The
// UPDATE only matches (and only then advances totp_last_step) when step is
// strictly greater than whatever is currently stored, or nothing is stored
// yet — so a concurrent or repeated call with the same or an older step
// affects zero rows and reports replay, with no separate read-then-write
// race window.
func (s *PgxAccountStore) ConsumeTOTPStep(ctx context.Context, accountID string, step int64) (bool, error) {
	ct, err := s.pool.Exec(ctx, `
UPDATE panel_accounts
SET totp_last_step = $1
WHERE id = $2 AND (totp_last_step IS NULL OR totp_last_step < $1)`, step, accountID)
	if err != nil {
		return false, fmt.Errorf("auth: consume totp step: %w", err)
	}
	return ct.RowsAffected() > 0, nil
}

// StoreRecoveryCodes implements TOTPStore. Transactional replace-all: the
// prior set (used or not) is deleted before the new set is inserted, so a
// caller never observes a mixed old/new set even under a mid-write failure
// (rollback restores the prior set entirely).
func (s *PgxAccountStore) StoreRecoveryCodes(ctx context.Context, accountID string, hashedCodes [][]byte) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("auth: store recovery codes: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM panel_totp_recovery_codes WHERE account_id = $1`, accountID); err != nil {
		return fmt.Errorf("auth: store recovery codes: clear existing: %w", err)
	}
	for _, h := range hashedCodes {
		if _, err := tx.Exec(ctx, `
INSERT INTO panel_totp_recovery_codes (account_id, code_hash) VALUES ($1, $2)`, accountID, h); err != nil {
			return fmt.Errorf("auth: store recovery codes: insert: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("auth: store recovery codes: commit: %w", err)
	}
	return nil
}

// ConsumeRecoveryCode implements TOTPStore: an atomic single-use claim.
// The UPDATE only matches an UNUSED code for accountID, so a concurrent
// double-submit of the same code can succeed at most once.
func (s *PgxAccountStore) ConsumeRecoveryCode(ctx context.Context, accountID string, hashedCode []byte) (bool, error) {
	ct, err := s.pool.Exec(ctx, `
UPDATE panel_totp_recovery_codes
SET used_at = now()
WHERE account_id = $1 AND code_hash = $2 AND used_at IS NULL`, accountID, hashedCode)
	if err != nil {
		return false, fmt.Errorf("auth: consume recovery code: %w", err)
	}
	return ct.RowsAffected() > 0, nil
}
