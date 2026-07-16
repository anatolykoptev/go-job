// Package auth provides pluggable session authentication for go-panel admin surfaces.
//
// The Authenticator interface abstracts session verification, login/logout handlers,
// and the Require middleware wrapper. Two implementations:
//
//   - HMACAuth: single-user HMAC cookie. Ships in foundations. Use for single-operator.
//   - BcryptTOTPAuth: multi-user bcrypt + roles + optional Redis login rate-limit;
//     TOTP 2FA is in progress (not yet wired — see the auth-hardening plan).
//
// Ported from go-nerv/internal/admin/auth.go (HMAC) and oxpulse-admin (BcryptTOTP stub).
package auth
