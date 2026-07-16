package resource

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/anatolykoptev/go-panel/auth"
	"github.com/anatolykoptev/go-panel/csrf"
)

// totpEnrollPageData is the view-model for totpEnrollContent.
type totpEnrollPageData struct {
	Secret     string
	OTPAuthURI string
	QRImageURL string
	QRPixels   string
	ConfirmURL string
	CSRFToken  string
	ErrorMsg   string
}

// renderEnrollForm renders the enrollment page: manual-entry secret,
// otpauth:// URI, QR <img>, and the code-confirmation form (a fresh CSRF
// token every render, matching resource.go's form-page convention).
func (e *totpEnrollment) renderEnrollForm(w http.ResponseWriter, r *http.Request, secret, uri, errMsg string) {
	d := totpEnrollPageData{
		Secret:     secret,
		OTPAuthURI: uri,
		QRImageURL: e.url("qr.png"),
		QRPixels:   strconv.Itoa(totpQRPixels),
		ConfirmURL: e.url("confirm"),
		CSRFToken:  csrf.Issue(e.panel.csrfKey, e.panel.sessionValue(r), csrf.DefaultTTL),
		ErrorMsg:   errMsg,
	}
	e.render(w, r, "Set Up Two-Factor Authentication", totpEnrollContent(d))
}

// reRenderEnrollWithError re-derives the pending secret (StartTOTPEnrollment
// is idempotent -- see its doc -- so this never mints a new one) and
// re-renders the enrollment page with errMsg, used after a failed Confirm.
func (e *totpEnrollment) reRenderEnrollWithError(w http.ResponseWriter, r *http.Request, acct *auth.Account, errMsg string) {
	secret, uri, err := auth.StartTOTPEnrollment(r.Context(), e.totpStore, e.encKey, e.issuer, acct)
	if err != nil {
		// Unexpected here -- we just successfully decrypted this exact
		// secret moments ago inside ConfirmTOTPEnrollment -- but fail
		// generically rather than silently drop the operator's error.
		slog.ErrorContext(r.Context(), "resource: re-render totp enroll after failed confirm", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	e.renderEnrollForm(w, r, secret, uri, errMsg)
}

// renderAlreadyEnabled shows the "2FA is already on" page for GET /enroll
// when acct.TOTPEnabled is already true.
func (e *totpEnrollment) renderAlreadyEnabled(w http.ResponseWriter, r *http.Request) {
	e.render(w, r, "Two-Factor Authentication", totpAlreadyEnabledContent(e.panel.basePath, e.url("disable")))
}

// renderConfirmSuccess shows the newly enabled 2FA + the recovery codes,
// exactly once.
func (e *totpEnrollment) renderConfirmSuccess(w http.ResponseWriter, r *http.Request, codes []string) {
	e.render(w, r, "Two-Factor Authentication Enabled", totpConfirmSuccessContent(e.panel.basePath, codes))
}

// renderReauthForm renders the shared password re-auth form used by both
// GET /disable and GET /regenerate (and their failed-POST re-render),
// parameterized by which action it posts back to.
func (e *totpEnrollment) renderReauthForm(w http.ResponseWriter, r *http.Request, actionSuffix, title, submitLabel, errMsg string) {
	tok := csrf.Issue(e.panel.csrfKey, e.panel.sessionValue(r), csrf.DefaultTTL)
	e.render(w, r, title, totpReauthFormContent(e.url(actionSuffix), tok, errMsg, title, submitLabel))
}

// renderDisabled shows the "2FA disabled" confirmation page.
func (e *totpEnrollment) renderDisabled(w http.ResponseWriter, r *http.Request) {
	e.render(w, r, "Two-Factor Authentication Disabled", totpDisabledContent(e.panel.basePath))
}

// renderRegenerateSuccess shows the freshly rotated recovery codes, exactly
// once.
func (e *totpEnrollment) renderRegenerateSuccess(w http.ResponseWriter, r *http.Request, codes []string) {
	e.render(w, r, "Recovery Codes Regenerated", totpRegenerateSuccessContent(e.panel.basePath, codes))
}
