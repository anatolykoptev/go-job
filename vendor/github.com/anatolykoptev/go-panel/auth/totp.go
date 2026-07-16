package auth

import (
	"bytes"
	"fmt"
	"image/png"
	"net/url"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// DefaultTOTPSkew is the recommended +/-period tolerance for clock drift
// when validating a code (ValidateTOTPCode's skew parameter). skew=1
// accepts a code from one period (30s) before or after the current time —
// matching both pquerna/otp's own totp.Validate default and Google
// Authenticator's documented tolerance. Values above 1 widen the guessable
// window and are "likely sketchy" per pquerna/otp's own doc comment.
//
// DO NOT use this for a call whose true result feeds
// TOTPStore.ConsumeTOTPStep (a login or enrollment-confirm verification
// step) — see ValidateTOTPCode's doc for why pairing skew>0 with the
// replay guard opens a replay window, and what to pass instead.
const DefaultTOTPSkew uint = 1

// totpPeriod is the RFC 6238 rotation period in seconds. Left at the
// library default (30s) for maximum authenticator-app compatibility —
// TOTPStepAt derives the replay-guard step from this SAME constant so the
// two can never drift apart.
const totpPeriod = 30

// GenerateTOTPSecret mints a new RFC 6238 TOTP key for accountName under
// issuer, using pquerna/otp's library defaults (SHA1, 6 digits, 30s
// period, 160-bit secret). Defaults are intentionally NOT overridden: every
// mainstream authenticator app (Google Authenticator, Authy, 1Password,
// Microsoft Authenticator, ...) assumes them, and deviating risks an app
// silently computing the wrong code with no error surfaced to the user.
//
// issuer and accountName are both required — pquerna/otp's Generate
// returns an error otherwise, since they're what the authenticator app
// displays to identify the account (e.g. "Issuer: AccountName" in the
// app's list).
//
// The returned Key's Secret() is the base32 plaintext to pass to
// EncryptTOTPSecret for storage. Its Image()/URL() are for one-time
// enrollment display (QR + otpauth:// text fallback) — see GenerateQRPNG.
// GenerateTOTPSecret must be called exactly once per enrollment attempt;
// re-displaying the QR for an already-pending secret goes through
// RebuildTOTPKey instead, never a second call to this function (which
// would mint a DIFFERENT secret and silently orphan the first).
func GenerateTOTPSecret(issuer, accountName string) (*otp.Key, error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: accountName,
	})
	if err != nil {
		return nil, fmt.Errorf("auth: generate totp secret: %w", err)
	}
	return key, nil
}

// RebuildTOTPKey reconstructs the *otp.Key for an already-minted secret —
// e.g. to re-render the QR code (GenerateQRPNG) on a reloaded enrollment
// page without minting a NEW secret and orphaning the one already written
// to the store. secret is the base32 string as returned by
// GenerateTOTPSecret's Key.Secret(): the decrypted, currently-pending
// stored secret. Never pass a freshly generated secret here — that is
// GenerateTOTPSecret's job.
func RebuildTOTPKey(issuer, accountName, secret string) (*otp.Key, error) {
	v := url.Values{}
	v.Set("secret", secret)
	v.Set("issuer", issuer)
	v.Set("algorithm", otp.AlgorithmSHA1.String())
	v.Set("digits", otp.DigitsSix.String())
	v.Set("period", "30")
	uri := fmt.Sprintf("otpauth://totp/%s?%s", url.PathEscape(issuer+":"+accountName), v.Encode())
	key, err := otp.NewKeyFromURL(uri)
	if err != nil {
		return nil, fmt.Errorf("auth: rebuild totp key: %w", err)
	}
	return key, nil
}

// GenerateQRPNG renders key's otpauth:// URI as a PNG QR code of size
// width x height pixels, suitable for an <img> enrollment display. The
// otpauth:// URI text itself (key.URL()) is the guaranteed manual-entry
// fallback and has no CSP dependency; a same-origin PNG needs img-src
// 'self'.
func GenerateQRPNG(key *otp.Key, width, height int) ([]byte, error) {
	img, err := key.Image(width, height)
	if err != nil {
		return nil, fmt.Errorf("auth: generate totp qr image: %w", err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("auth: encode totp qr png: %w", err)
	}
	return buf.Bytes(), nil
}

// ValidateTOTPCode reports whether code is a valid RFC 6238 TOTP code for
// secret (base32, as returned by GenerateTOTPSecret's Key.Secret()) at the
// current time, allowing +/-skew periods (30s each) of clock drift.
// Comparison is constant-time: pquerna/otp's hotp.ValidateCustom compares
// the computed and submitted codes via crypto/subtle.ConstantTimeCompare
// (verified against the pquerna/otp v1.5.0 source, hotp/hotp.go).
//
// This checks CODE VALIDITY ONLY — it does not consult or advance the
// replay guard (TOTPStore.ConsumeTOTPStep).
//
// # Pairing with ConsumeTOTPStep — READ BEFORE WIRING A VERIFY/LOGIN PATH
//
// A caller that treats a true result as authoritative (login, enrollment
// confirm — anywhere ConsumeTOTPStep is then called to enforce single-use)
// MUST call with skew=0 and then consume EXACTLY TOTPStepAt(t) (the same t
// passed here). Do NOT pass DefaultTOTPSkew, or any skew>0, to a call
// paired with ConsumeTOTPStep(TOTPStepAt(now())) — that pairing is a
// replay hole, not merely a redundant check:
//
// With skew=1, a code is accepted for any of steps {N-1, N, N+1} relative
// to the CURRENT step N — but the step you'd consume from
// TOTPStepAt(now()) is always N, regardless of which of the three the
// submitted code actually matched. Concretely: a code used (and consumed
// at step N-1) is STILL accepted one period later, because real time has
// moved to step N and N-1 is back inside the skew=1 window — and
// ConsumeTOTPStep(N) STILL SUCCEEDS, because N > N-1 looks like forward
// progress to the monotonic guard. Net effect: the same code is usable
// twice, up to ~30-60s apart — exactly the property TOTP exists to
// prevent, on what is a money-path admin login.
//
// If wider clock-drift tolerance is genuinely required, the caller must
// determine WHICH specific step matched — e.g. call ValidateTOTPCodeAt
// with skew=0 at each of t-Period, t, t+Period in turn — and consume THAT
// step, never a step derived from t alone when more than one step could
// have produced the accepted code.
func ValidateTOTPCode(secret, code string, skew uint) bool {
	return ValidateTOTPCodeAt(secret, code, skew, time.Now())
}

// ValidateTOTPCodeAt is ValidateTOTPCode with an explicit time, for
// deterministic tests (RFC 6238 known-answer vectors, skew-window
// boundaries) and any future caller needing to validate against a
// non-"now" instant. See ValidateTOTPCode's doc for the mandatory skew=0
// pairing with ConsumeTOTPStep — it applies identically here.
func ValidateTOTPCodeAt(secret, code string, skew uint, t time.Time) bool {
	valid, err := totp.ValidateCustom(code, secret, t, totp.ValidateOpts{
		Period:    totpPeriod,
		Skew:      skew,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		return false // fail closed: malformed code/secret is not a valid match
	}
	return valid
}

// TOTPStepAt returns the RFC 6238 time-step counter for t at the standard
// 30-second period — the value TOTPStore.ConsumeTOTPStep's replay guard
// persists and compares. Exported so a caller derives the step from the
// SAME period this package validates against (totpPeriod), rather than
// re-deriving floor(unix/30) itself and risking drift between the two.
//
// TOTPStepAt(t) is only the RIGHT step to pass to ConsumeTOTPStep when
// ValidateTOTPCodeAt was called with skew=0 at that same t — see
// ValidateTOTPCode's doc for why a skew>0 validation can accept a code
// belonging to a DIFFERENT step than TOTPStepAt(t) would derive.
func TOTPStepAt(t time.Time) int64 {
	return t.Unix() / totpPeriod
}
