package auth

import (
	"context"
	"errors"

	"golang.org/x/crypto/bcrypt"
)

// ErrAccountNotFound is returned by AccountStore lookups when no matching,
// active account exists. Callers can errors.Is against it.
var ErrAccountNotFound = errors.New("auth: account not found")

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
