package resource

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/anatolykoptev/go-panel/auth"
	"github.com/anatolykoptev/go-panel/shell"
)

// verifyCSRF parses the request form (capped, see parseForm) and verifies
// its CSRF token against the panel's key and the request's session cookie
// value -- the EXACT mechanism protecting every other write route in the
// framework, via the shared Panel.verifyCSRFToken (resource.go) that also
// backs saveHandler and MountAction's csrfProtect. Writes the appropriate
// error response and returns false on any failure; callers must stop
// processing.
func (e *totpEnrollment) verifyCSRF(w http.ResponseWriter, r *http.Request) bool {
	if !e.parseForm(w, r) {
		return false
	}
	return e.panel.verifyCSRFToken(w, r, "resource: totp CSRF verification failed")
}

// enrollStart serves GET {prefix}/enroll (renders the password re-auth gate)
// and POST {prefix}/enroll (verifies the password, then generates once or
// redisplays the pending TOTP secret and renders the enrollment page: secret
// text, otpauth:// URI, QR <img>, and the code-confirmation form). An
// already-enabled account is routed to a "2FA is on, disable first" page
// instead — no password demanded for a guaranteed no-op.
//
// The re-auth step is the same gate disable/regenerate enforce: enrollment
// mints a durable TOTP seed and displays it in plaintext, so a bare live
// session (a stolen cookie) must never reach it. VerifyAccountPassword runs
// inside StartTOTPEnrollmentWithReauth — password failure never touches the
// pending-secret state.
func (e *totpEnrollment) enrollStart(w http.ResponseWriter, r *http.Request) {
	shell.SecurityHeaders(w)
	acct, ok := e.currentAccount(w, r)
	if !ok {
		return
	}
	if acct.TOTPEnabled {
		e.renderAlreadyEnabled(w, r)
		return
	}
	if r.Method != http.MethodPost {
		e.renderReauthForm(w, r, "enroll", "Set Up Two-Factor Authentication", "Continue", "")
		return
	}
	if !e.verifyCSRF(w, r) {
		return
	}
	pw := r.FormValue("current_password") //nolint:gosec // G120 false positive: verifyCSRF (above) already ran parseForm's http.MaxBytesReader cap before any FormValue read
	secret, uri, err := auth.StartTOTPEnrollmentWithReauth(r.Context(), e.accountStore, e.totpStore, e.encKey, e.issuer, acct, pw)
	if errors.Is(err, auth.ErrReauthFailed) {
		e.renderReauthForm(w, r, "enroll", "Set Up Two-Factor Authentication", "Continue", "Incorrect password.")
		return
	}
	if errors.Is(err, auth.ErrTOTPAlreadyEnabled) {
		// Enabled between the form render and this POST — same destination as
		// the GET guard above.
		e.renderAlreadyEnabled(w, r)
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "resource: start totp enrollment", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	e.renderEnrollForm(w, r, secret, uri, "")
}

// qrImage serves GET {prefix}/qr.png: the same-origin PNG an enrollment
// page's <img> tag points at. Re-derives the QR from the CURRENT account's
// OWN pending secret (auth.SessionFrom-scoped, see currentAccount) -- never
// any other account's. Served ONLY while enrollment is pending: once
// confirmed the seed has no legitimate reason to be displayed again (a
// re-enroll goes through disable → fresh pending secret), and a live session
// — e.g. a stolen cookie — must not be enough to re-extract it.
func (e *totpEnrollment) qrImage(w http.ResponseWriter, r *http.Request) {
	shell.SecurityHeaders(w)
	acct, ok := e.currentAccount(w, r)
	if !ok {
		return
	}
	if acct.TOTPEnabled {
		http.NotFound(w, r)
		return
	}
	png, err := auth.BuildTOTPQRPNG(r.Context(), e.totpStore, e.encKey, e.issuer, acct, totpQRPixels, totpQRPixels)
	if errors.Is(err, auth.ErrTOTPNotEnrolled) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "resource: build totp qr png", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png) //nolint:gosec // G705 false positive: png is auth.GenerateQRPNG's binary PNG output served with an explicit Content-Type: image/png above -- not HTML/JS a browser would execute
}

// confirm serves POST {prefix}/confirm: the code AND current password
// submitted from enrollStart's form. Success flips the enrollment on and
// shows the operator their recovery codes exactly once. Password re-auth
// runs before code validation (ConfirmTOTPEnrollmentWithReauth): a wrong
// password returns ErrReauthFailed — enrollment state untouched, the
// operator routed BACK through the enroll re-auth gate — and failure (wrong
// code, malformed code, or a code whose step was already consumed -- see
// auth.ErrTOTPCodeInvalid) re-renders the SAME enrollment page with a
// generic error, never flipping totp_enabled.
func (e *totpEnrollment) confirm(w http.ResponseWriter, r *http.Request) {
	shell.SecurityHeaders(w)
	acct, ok := e.currentAccount(w, r)
	if !ok {
		return
	}
	if !e.verifyCSRF(w, r) {
		return
	}
	code := r.FormValue("code")           //nolint:gosec // G120 false positive: verifyCSRF (above) already ran parseForm's http.MaxBytesReader cap before any FormValue read
	pw := r.FormValue("current_password") //nolint:gosec // same G120 false positive
	codes, err := auth.ConfirmTOTPEnrollmentWithReauth(r.Context(), e.accountStore, e.totpStore, e.encKey, acct, code, pw, time.Now())
	if errors.Is(err, auth.ErrReauthFailed) {
		// Never re-render the secret on a failed re-auth — the pending seed
		// must stay invisible until the password verifies (enrollStart's
		// gate is the single page that discloses it).
		e.renderReauthForm(w, r, "enroll", "Set Up Two-Factor Authentication", "Continue", "Incorrect password.")
		return
	}
	if errors.Is(err, auth.ErrTOTPAlreadyEnabled) {
		e.renderAlreadyEnabled(w, r)
		return
	}
	if errors.Is(err, auth.ErrTOTPCodeInvalid) {
		e.reRenderEnrollWithError(w, r, acct, "Invalid or already-used code. Enter the current code from your authenticator app.")
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "resource: confirm totp enrollment", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	e.renderConfirmSuccess(w, r, codes)
}

// disable serves GET {prefix}/disable (renders the password re-auth form)
// and POST {prefix}/disable (processes it): clears the account's entire TOTP
// enrollment (secret, recovery codes, replay-guard step) after the operator
// re-proves their current password. See auth.DisableTOTPWithReauth's doc for
// why a bare click can never disable 2FA, and why password (not a live TOTP
// code) is the re-auth factor.
func (e *totpEnrollment) disable(w http.ResponseWriter, r *http.Request) {
	shell.SecurityHeaders(w)
	acct, ok := e.currentAccount(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		e.renderReauthForm(w, r, "disable", "Disable Two-Factor Authentication", "Disable 2FA", "")
		return
	}
	if !e.verifyCSRF(w, r) {
		return
	}
	pw := r.FormValue("current_password") //nolint:gosec // G120 false positive: verifyCSRF (above) already ran parseForm's http.MaxBytesReader cap before any FormValue read
	err := auth.DisableTOTPWithReauth(r.Context(), e.accountStore, e.totpStore, acct, pw)
	if errors.Is(err, auth.ErrReauthFailed) {
		e.renderReauthForm(w, r, "disable", "Disable Two-Factor Authentication", "Disable 2FA", "Incorrect password.")
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "resource: disable totp", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	e.renderDisabled(w, r)
}

// regenerate serves GET {prefix}/regenerate (renders the password re-auth
// form) and POST {prefix}/regenerate (processes it): replaces the account's
// ENTIRE recovery-code set (every prior code stops working --
// StoreRecoveryCodes's documented replace-all semantics) after password
// re-auth, showing the new codes exactly once.
func (e *totpEnrollment) regenerate(w http.ResponseWriter, r *http.Request) {
	shell.SecurityHeaders(w)
	acct, ok := e.currentAccount(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		e.renderReauthForm(w, r, "regenerate", "Regenerate Recovery Codes", "Regenerate Codes", "")
		return
	}
	if !e.verifyCSRF(w, r) {
		return
	}
	pw := r.FormValue("current_password") //nolint:gosec // G120 false positive: verifyCSRF (above) already ran parseForm's http.MaxBytesReader cap before any FormValue read
	codes, err := auth.RegenerateRecoveryCodesWithReauth(r.Context(), e.accountStore, e.totpStore, acct, pw)
	if errors.Is(err, auth.ErrReauthFailed) {
		e.renderReauthForm(w, r, "regenerate", "Regenerate Recovery Codes", "Regenerate Codes", "Incorrect password.")
		return
	}
	if errors.Is(err, auth.ErrTOTPNotEnabled) {
		http.Error(w, "Two-factor authentication is not enabled", http.StatusBadRequest)
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "resource: regenerate totp recovery codes", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	e.renderRegenerateSuccess(w, r, codes)
}
