package auth

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrTOTPAlreadyEnabled is returned by StartTOTPEnrollment when the account
// already has a CONFIRMED TOTP enrollment (Account.TOTPEnabled == true).
// Re-enrolling over an active 2FA setup without an explicit Disable first
// would silently rotate the secret out from under an operator who believes
// their existing authenticator app still works -- the caller must route to
// a "2FA already enabled, disable first" page instead of ever reaching
// GenerateTOTPSecret/SetPendingTOTPSecret in this state.
var ErrTOTPAlreadyEnabled = errors.New("auth: totp already enabled for this account")

// ErrTOTPCodeInvalid is returned by ConfirmTOTPEnrollment when the submitted
// code fails RFC 6238 validation OR fails the replay guard (ConsumeTOTPStep
// reports the step already used). Both cases collapse to one error
// deliberately -- mirrors ErrTOTPDecryptFailed's rationale (totp_crypto.go):
// distinguishing "wrong code" from "correct code, already used" would hand
// an attacker a free oracle for probing whether a guessed code was ever
// valid, on a fail-closed 2FA confirm path.
var ErrTOTPCodeInvalid = errors.New("auth: totp code invalid or already used")

// ErrReauthFailed is returned by DisableTOTPWithReauth and
// RegenerateRecoveryCodesWithReauth when the supplied current-password
// re-authentication does not match the account.
var ErrReauthFailed = errors.New("auth: re-authentication failed")

// StartTOTPEnrollment ensures a PENDING (unconfirmed) TOTP secret exists for
// acct and returns its base32 secret text and otpauth:// URI for display --
// the data an enrollment page needs to show the manual-entry fallback and
// build the QR code (see BuildTOTPQRPNG).
//
// Idempotent by design, safe to call on every GET of the enrollment page:
//   - acct.TOTPEnabled == true (already confirmed) -> ErrTOTPAlreadyEnabled,
//     never touches the store. A confirmed enrollment must go through
//     DisableTOTPWithReauth before a new one can start.
//   - a pending secret already exists (a prior GET already minted one) ->
//     RebuildTOTPKey reconstructs the SAME key from the already-stored
//     secret. GenerateTOTPSecret is never called a second time for the same
//     enrollment attempt, so a page reload can never orphan a secret the
//     operator has already started scanning into their authenticator app.
//   - neither -> GenerateTOTPSecret mints a new secret exactly once, encrypts
//     it, and persists it via SetPendingTOTPSecret before returning -- so the
//     very next call (e.g. a reload) takes the "pending exists" branch above.
func StartTOTPEnrollment(ctx context.Context, store TOTPStore, encKey []byte, issuer string, acct *Account) (secret, otpauthURI string, err error) {
	if acct.TOTPEnabled {
		return "", "", ErrTOTPAlreadyEnabled
	}
	encrypted, err := store.GetTOTPSecret(ctx, acct.ID)
	switch {
	case errors.Is(err, ErrTOTPNotEnrolled):
		return mintPendingTOTPSecret(ctx, store, encKey, issuer, acct)
	case err != nil:
		return "", "", fmt.Errorf("auth: start totp enrollment: %w", err)
	default:
		return rebuildPendingTOTPSecret(encrypted, encKey, issuer, acct)
	}
}

// mintPendingTOTPSecret generates a brand-new secret and persists it as the
// pending enrollment. Split out of StartTOTPEnrollment to keep both branches
// small and independently readable.
func mintPendingTOTPSecret(ctx context.Context, store TOTPStore, encKey []byte, issuer string, acct *Account) (secret, otpauthURI string, err error) {
	key, err := GenerateTOTPSecret(issuer, acct.Email)
	if err != nil {
		return "", "", err
	}
	encrypted, err := EncryptTOTPSecret([]byte(key.Secret()), encKey)
	if err != nil {
		return "", "", err
	}
	if err := store.SetPendingTOTPSecret(ctx, acct.ID, encrypted); err != nil {
		return "", "", fmt.Errorf("auth: persist pending totp secret: %w", err)
	}
	return key.Secret(), key.URL(), nil
}

// rebuildPendingTOTPSecret decrypts an already-stored pending secret and
// rebuilds its *otp.Key for redisplay, without minting a new one.
func rebuildPendingTOTPSecret(encrypted, encKey []byte, issuer string, acct *Account) (secret, otpauthURI string, err error) {
	decrypted, err := DecryptTOTPSecret(encrypted, encKey)
	if err != nil {
		return "", "", err
	}
	key, err := RebuildTOTPKey(issuer, acct.Email, string(decrypted))
	if err != nil {
		return "", "", err
	}
	return key.Secret(), key.URL(), nil
}

// BuildTOTPQRPNG re-derives acct's pending or confirmed TOTP key from the
// store and renders it as a PNG QR code (see GenerateQRPNG). Returns
// ErrAccountNotFound / ErrTOTPNotEnrolled unchanged (callers distinguish "no
// such account" from "nothing to show yet" via errors.Is); any other store
// or crypto failure comes back wrapped.
func BuildTOTPQRPNG(ctx context.Context, store TOTPStore, encKey []byte, issuer string, acct *Account, width, height int) ([]byte, error) {
	encrypted, err := store.GetTOTPSecret(ctx, acct.ID)
	if err != nil {
		return nil, err
	}
	decrypted, err := DecryptTOTPSecret(encrypted, encKey)
	if err != nil {
		return nil, err
	}
	key, err := RebuildTOTPKey(issuer, acct.Email, string(decrypted))
	if err != nil {
		return nil, err
	}
	return GenerateQRPNG(key, width, height)
}

// totpConfirmSkew is the ONLY skew value safe to pair with ConsumeTOTPStep's
// replay guard -- see ValidateTOTPCode's doc comment in totp.go for the full
// replay-window rationale. Never widen this without also changing how the
// consumed step is derived.
const totpConfirmSkew = 0

// ConfirmTOTPEnrollment validates code against acct's pending secret at
// totpConfirmSkew and, only on success, atomically consumes that exact
// step, mints RecoveryCodeCount recovery codes, and confirms the
// enrollment. Returns the recovery codes IN PLAINTEXT: the caller must
// render them to the operator exactly once and never persist them
// (StoreRecoveryCodes already wrote the hashed form).
//
// On any failure (wrong code, malformed code, OR a replayed code that fails
// the ConsumeTOTPStep guard) this returns ErrTOTPCodeInvalid and the
// account's enrollment state is UNCHANGED -- totp_enabled stays false, no
// recovery codes are generated.
//
// Ordering is deliberate: recovery codes are stored BEFORE the enrollment is
// confirmed. If StoreRecoveryCodes fails, totp_enabled is still false -- the
// account can never be observed as "2FA enabled with zero recovery codes"
// from a partial failure here. Worst case on a failure between the two
// steps: the submitted code is burned (consumed by the replay guard) and the
// operator must Confirm again with a fresh code -- safe, if mildly annoying,
// never a security gap.
//
// Refuses (ErrTOTPAlreadyEnabled, same sentinel StartTOTPEnrollment uses)
// when acct.TOTPEnabled is already true. Without this check, an operator
// holding nothing more than a live session and a CURRENT code from their own
// already-enrolled authenticator could POST straight to Confirm and
// silently rotate their recovery-code set -- bypassing the password
// re-authentication RegenerateRecoveryCodesWithReauth deliberately requires
// for exactly that operation. A live TOTP code proves control of the
// authenticator device, not the account password; the two step-down
// mechanisms guard different secrets and must not substitute for each
// other.
func ConfirmTOTPEnrollment(ctx context.Context, store TOTPStore, encKey []byte, acct *Account, code string, now time.Time) ([]string, error) {
	if acct.TOTPEnabled {
		return nil, ErrTOTPAlreadyEnabled
	}
	encrypted, err := store.GetTOTPSecret(ctx, acct.ID)
	if err != nil {
		if errors.Is(err, ErrTOTPNotEnrolled) || errors.Is(err, ErrAccountNotFound) {
			return nil, ErrTOTPCodeInvalid
		}
		return nil, fmt.Errorf("auth: confirm totp enrollment: %w", err)
	}
	decrypted, err := DecryptTOTPSecret(encrypted, encKey)
	if err != nil {
		return nil, ErrTOTPCodeInvalid
	}
	if !ValidateTOTPCodeAt(string(decrypted), code, totpConfirmSkew, now) {
		return nil, ErrTOTPCodeInvalid
	}
	ok, err := store.ConsumeTOTPStep(ctx, acct.ID, TOTPStepAt(now))
	if err != nil {
		return nil, fmt.Errorf("auth: consume totp step: %w", err)
	}
	if !ok {
		return nil, ErrTOTPCodeInvalid // replay: same generic error, no oracle
	}
	return confirmWithFreshRecoveryCodes(ctx, store, acct)
}

// confirmWithFreshRecoveryCodes atomically confirms enrollment and stores the
// freshly generated recovery codes once code validation has already succeeded.
func confirmWithFreshRecoveryCodes(ctx context.Context, store TOTPStore, acct *Account) ([]string, error) {
	codes, hashes, err := GenerateRecoveryCodes(RecoveryCodeCount)
	if err != nil {
		return nil, fmt.Errorf("auth: generate recovery codes: %w", err)
	}
	if err := store.ConfirmTOTPEnrollmentWithRecoveryCodes(ctx, acct.ID, hashes); err != nil {
		return nil, fmt.Errorf("auth: confirm totp enrollment with recovery codes: %w", err)
	}
	return codes, nil
}

// VerifyAccountPassword re-authenticates email+password against store,
// independent of any live session -- the step-down confirmation
// DisableTOTPWithReauth and RegenerateRecoveryCodesWithReauth require before
// touching an account's 2FA state. Unlike BcryptTOTPAuth's login path, this
// does NOT apply a dummy-hash timing equalization: that defense protects an
// ANONYMOUS caller from enumerating which emails exist, which does not apply
// here -- email is always the CURRENT authenticated session's own (never
// attacker-supplied), so there is nothing to enumerate.
func VerifyAccountPassword(ctx context.Context, store AccountStore, email, password string) bool {
	acct, err := store.GetByEmail(ctx, email)
	if err != nil {
		return false
	}
	return VerifyPassword(password, acct.PasswordHash)
}

// DisableTOTPWithReauth clears acct's TOTP enrollment (secret, recovery
// codes, replay-guard step) after re-verifying password against
// accountStore. Returns ErrReauthFailed (state unchanged) on a wrong
// password -- 2FA can never be disabled by a bare unauthenticated click,
// only by an operator who can currently reproduce their password. Password
// (not a live TOTP code) is the re-auth factor deliberately: the operator
// who most needs to disable 2FA is often the one who just lost the device
// that would produce a fresh code.
func DisableTOTPWithReauth(ctx context.Context, accountStore AccountStore, totpStore TOTPStore, acct *Account, password string) error {
	if !VerifyAccountPassword(ctx, accountStore, acct.Email, password) {
		return ErrReauthFailed
	}
	return totpStore.DisableTOTP(ctx, acct.ID)
}

// RegenerateRecoveryCodesWithReauth replaces acct's ENTIRE recovery-code set
// (StoreRecoveryCodes's documented replace-all semantics -- every prior
// code, used or not, stops working) after re-verifying password. Returns the
// new codes IN PLAINTEXT for exactly-once display; see
// ConfirmTOTPEnrollment's doc for the same one-time-display contract.
func RegenerateRecoveryCodesWithReauth(ctx context.Context, accountStore AccountStore, totpStore TOTPStore, acct *Account, password string) ([]string, error) {
	if !acct.TOTPEnabled {
		return nil, ErrTOTPNotEnabled
	}
	if !VerifyAccountPassword(ctx, accountStore, acct.Email, password) {
		return nil, ErrReauthFailed
	}
	codes, hashes, err := GenerateRecoveryCodes(RecoveryCodeCount)
	if err != nil {
		return nil, fmt.Errorf("auth: generate recovery codes: %w", err)
	}
	if err := totpStore.StoreRecoveryCodes(ctx, acct.ID, hashes); err != nil {
		return nil, fmt.Errorf("auth: store recovery codes: %w", err)
	}
	return codes, nil
}
