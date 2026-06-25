// Package csrf provides double-submit CSRF protection for go-panel write forms.
//
// # Wire format
//
//	token = expiry "|" hex(HMAC-SHA256(key, sessionValue "|" expiry))
//
// The expiry (Unix seconds, decimal) appears FIRST. The MAC covers both the
// session binding value and the expiry, preventing either from being tampered.
//
// Example: "1735689600|ab8662f5..."
//
// # Binding
//
// The CSRF token is bound to the full session cookie value at issue time.
// HMACAuth v2 embeds a per-login random nonce in the cookie value, so every
// new login produces a distinct cookie value and thereby invalidates all CSRF
// tokens that were bound to the previous session. Logout + re-login = full
// CSRF token rotation (SEC-CR-002 resolved).
//
// # Key lifecycle
//
// The CSRFKey is passed via resource.Config.CSRFKey. Register panics at startup
// if CSRFKey is shorter than 32 bytes or if a Writer-enabled resource is
// configured without a key (fail-closed).
//
// # Usage
//
//	token := csrf.Issue(key, sessionCookieValue, ttl)
//	// embed token in the form hidden input named "_csrf"
//	if err := csrf.Verify(key, sessionCookieValue, formToken); err != nil {
//	    http.Error(w, "invalid CSRF token", http.StatusForbidden)
//	    return
//	}
package csrf

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// FormField is the hidden input name for CSRF tokens.
	FormField = "_csrf"
	// DefaultTTL is the default CSRF token lifetime.
	DefaultTTL = 2 * time.Hour
	// maxFutureTTL is the upper ceiling on token expiry accepted by Verify.
	// Tokens claiming an expiry more than 24h in the future are rejected,
	// regardless of whether the MAC is valid.
	maxFutureTTL = 24 * time.Hour
)

var (
	// ErrMissing is returned when no token is present.
	ErrMissing = errors.New("csrf: token missing")
	// ErrExpired is returned when the token has expired.
	ErrExpired = errors.New("csrf: token expired")
	// ErrInvalid is returned when the token signature does not match or the token is malformed.
	ErrInvalid = errors.New("csrf: token invalid")
)

// Issue generates a signed CSRF token bound to the session cookie value.
// The returned string is safe to embed in a hidden HTML input.
func Issue(key []byte, sessionValue string, ttl time.Duration) string {
	exp := time.Now().Add(ttl).Unix()
	sig := sign(key, sessionValue, exp)
	return fmt.Sprintf("%d|%s", exp, sig)
}

// Verify checks the token against the session cookie value.
// Returns ErrMissing, ErrExpired, or ErrInvalid on failure; nil on success.
//
// Additional checks beyond basic expiry:
//   - exp > now+maxFutureTTL → ErrInvalid (issuer TTL ceiling, SEC-CR-003)
//   - MAC mismatch → ErrInvalid (constant-time compare)
func Verify(key []byte, sessionValue, token string) error {
	if token == "" {
		return ErrMissing
	}
	sep := strings.IndexByte(token, '|')
	if sep < 0 {
		return ErrInvalid
	}
	exp, err := strconv.ParseInt(token[:sep], 10, 64)
	if err != nil {
		return ErrInvalid
	}
	now := time.Now().Unix()
	if now > exp {
		return ErrExpired
	}
	// Ceiling: reject tokens claiming expiry more than maxFutureTTL ahead.
	if exp > now+int64(maxFutureTTL/time.Second) {
		return ErrInvalid
	}
	got := token[sep+1:]
	expected := sign(key, sessionValue, exp)
	if !hmac.Equal([]byte(got), []byte(expected)) {
		return ErrInvalid
	}
	return nil
}

// sign returns the hex-encoded HMAC-SHA256 of "sessionValue|expiry".
func sign(key []byte, sessionValue string, exp int64) string {
	mac := hmac.New(sha256.New, key)
	_, _ = fmt.Fprintf(mac, "%s|%d", sessionValue, exp)
	return hex.EncodeToString(mac.Sum(nil))
}
