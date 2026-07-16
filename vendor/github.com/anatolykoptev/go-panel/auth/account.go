package auth

import (
	"context"
	"errors"

	"golang.org/x/crypto/bcrypt"
)

// ErrAccountNotFound is returned by AccountStore lookups when no matching,
// active account exists. Callers can errors.Is against it.
var ErrAccountNotFound = errors.New("auth: account not found")

// ErrTOTPNotEnrolled is returned by TOTPStore.GetTOTPSecret when the
// account row exists but has no totp_secret set (never enrolled, or
// DisableTOTP already cleared it). Callers can errors.Is against it to
// distinguish "no secret to decrypt" from ErrAccountNotFound ("no such
// account") and from a transport/decryption error.
var ErrTOTPNotEnrolled = errors.New("auth: no totp secret set for account")

// ErrTOTPNotEnabled is returned by recovery-code management helpers when an
// operation requires an already-confirmed TOTP enrollment but the account's
// totp_enabled flag is false.
var ErrTOTPNotEnabled = errors.New("auth: totp is not enabled for account")

// Account is a panel operator account for multi-user auth. Ported in shape from
// oxpulse-admin's accounts row. PasswordHash is a bcrypt hash, never plaintext;
// it is only populated by lookups that select it (GetByEmail), left empty otherwise.
type Account struct {
	ID           string // UUID (text)
	Email        string
	Name         string
	PasswordHash string
	Role         string
	Active       bool
	TOTPEnabled  bool
}

// AccountStore is the persistence seam for multi-user operator auth. The
// framework ships PgxAccountStore (over pgxpool); a consumer may provide its own.
type AccountStore interface {
	// GetByEmail returns the active account WITH its password hash for email, or
	// ErrAccountNotFound. Login hot path.
	GetByEmail(ctx context.Context, email string) (*Account, error)
	// GetByID returns the current account state for id (no password hash), used to
	// re-validate a live session against the DB. The row is returned even when
	// Active is false — a caller doing a revocation check MUST inspect
	// Account.Active; ErrAccountNotFound is returned only when no row exists.
	GetByID(ctx context.Context, id string) (*Account, error)
	// UpdateLastLogin stamps last_login_at = now() for id.
	UpdateLastLogin(ctx context.Context, id string) error
	// UpdatePasswordHash sets a new bcrypt hash for id; ErrAccountNotFound if no row.
	UpdatePasswordHash(ctx context.Context, id, passwordHash string) error
	// CreateAccount inserts an account, returning (id, created). created is false
	// when an account with that email already exists (idempotent seed).
	CreateAccount(ctx context.Context, email, name, passwordHash, role string) (id string, created bool, err error)
}

// TOTPStore is the persistence seam for RFC 6238 2FA: the encrypted TOTP
// secret's set-but-unconfirmed enrollment sequence, the monotonic replay
// guard, and single-use recovery codes. It is declared SEPARATELY from
// AccountStore (rather than folded into it) so an external AccountStore
// implementer who doesn't want TOTP support is unaffected — AccountStore's
// contract does not change. The framework-provided PgxAccountStore
// implements both; callers/hosts type-assert
// (store.(TOTPStore)) to discover TOTP support, which is exactly what
// NewBcryptTOTPAuth's setup panic does (see bcrypt_auth.go).
//
// All encryptedSecret / encrypted-secret return values are the raw
// EncryptTOTPSecret/DecryptTOTPSecret ciphertext bytes (nonce||ciphertext)
// — callers never see plaintext through this interface. All hashedCode /
// hashedCodes values are HashRecoveryCode output; callers never pass a
// plaintext recovery code through this interface either.
type TOTPStore interface {
	// SetPendingTOTPSecret writes encryptedSecret and sets totp_enabled to
	// false — the enroll-start step. Any prior secret/enrollment state for
	// the account is overwritten (a re-enrollment attempt discards the
	// previous pending secret). ErrAccountNotFound if id doesn't exist.
	SetPendingTOTPSecret(ctx context.Context, accountID string, encryptedSecret []byte) error
	// ConfirmTOTPEnrollment flips totp_enabled to true and stamps
	// totp_enrolled_at = now(). Callers must have already verified a live
	// TOTP code (ValidateTOTPCode) AND consumed its step (ConsumeTOTPStep)
	// before calling this — it performs no code verification itself.
	// ErrAccountNotFound if id doesn't exist.
	//
	// Deprecated: use ConfirmTOTPEnrollmentWithRecoveryCodes to atomically
	// confirm enrollment and store recovery codes in one transaction.
	ConfirmTOTPEnrollment(ctx context.Context, accountID string) error
	// ConfirmTOTPEnrollmentWithRecoveryCodes atomically confirms TOTP
	// enrollment and replaces the account's recovery codes with hashedCodes.
	// Either both operations succeed or neither is persisted, preventing an
	// enabled account from being left without usable recovery codes.
	// ErrAccountNotFound if id doesn't exist.
	ConfirmTOTPEnrollmentWithRecoveryCodes(ctx context.Context, accountID string, hashedCodes [][]byte) error
	// GetTOTPSecret returns the stored encrypted secret (pending or
	// confirmed — callers distinguish via Account.TOTPEnabled) for QR
	// re-display or code verification. ErrAccountNotFound if id doesn't
	// exist; ErrTOTPNotEnrolled if the account exists but has no secret set.
	GetTOTPSecret(ctx context.Context, accountID string) ([]byte, error)
	// DisableTOTP clears the secret, enrollment state, replay-guard step,
	// AND every stored recovery code for the account (a full reset, so a
	// subsequent SetPendingTOTPSecret starts from a clean slate).
	// ErrAccountNotFound if id doesn't exist.
	DisableTOTP(ctx context.Context, accountID string) error
	// ConsumeTOTPStep atomically checks-and-advances the account's replay
	// guard: ok is true only if step is strictly greater than the
	// previously stored step (or none was stored yet), in which case step
	// is now persisted. A false return with a nil error means the code for
	// this (or an earlier) step was already consumed — REPLAY, not an
	// error. This guards single-use ONLY; it is not a guess-rate defense
	// (that is a RateLimiter, applied by the caller).
	ConsumeTOTPStep(ctx context.Context, accountID string, step int64) (ok bool, err error)
	// StoreRecoveryCodes atomically REPLACES the account's entire recovery
	// code set with hashedCodes (each HashRecoveryCode output). Any
	// previously stored codes, used or not, are deleted first.
	// ErrAccountNotFound if id doesn't exist.
	StoreRecoveryCodes(ctx context.Context, accountID string, hashedCodes [][]byte) error
	// ConsumeRecoveryCode atomically marks the recovery code matching
	// hashedCode as used, IF it exists for accountID and has not already
	// been used. ok is false (nil error) for an unknown or already-used
	// code — indistinguishable on purpose, so a caller can't use the
	// response to enumerate which codes remain valid.
	ConsumeRecoveryCode(ctx context.Context, accountID string, hashedCode []byte) (ok bool, err error)
}

// DefaultBcryptCost balances admin-login latency against brute-force resistance.
const DefaultBcryptCost = 12

// HashPassword returns a bcrypt hash of password at DefaultBcryptCost.
func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), DefaultBcryptCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// VerifyPassword reports whether password matches the bcrypt hash. Returns false
// on any mismatch or malformed hash. Comparison is constant-time within bcrypt.
func VerifyPassword(password, hash string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
