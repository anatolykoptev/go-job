package resource

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/a-h/templ"
	"github.com/anatolykoptev/go-panel/auth"
)

// totpQRPixels is the width and height (in pixels) of the enrollment QR
// code's <img> display and the PNG qrImage serves.
const totpQRPixels = 256

// maxTOTPFormBytes caps every POST body this feature accepts (a 6-digit
// code, a password, and a CSRF token) -- generous for the content, small
// enough to reject an oversized body before it reaches ParseForm.
const maxTOTPFormBytes = 4096

// TOTPEnrollmentConfig configures MountTOTPEnrollment.
type TOTPEnrollmentConfig struct {
	// Store is the account persistence seam; it MUST also implement
	// auth.TOTPStore (auth.PgxAccountStore does) -- MountTOTPEnrollment
	// panics at setup otherwise (fail-closed, mirrors
	// auth.NewBcryptTOTPAuth's own Store-capability check).
	Store auth.AccountStore
	// TOTPEncryptionKey is the AES-256-GCM key for secret-at-rest; pass the
	// SAME key given to auth.BcryptConfig.TOTPEncryptionKey. Must be exactly
	// auth.TOTPEncryptionKeyLen (32) bytes.
	TOTPEncryptionKey []byte
	// Issuer is the app name shown in the operator's authenticator app
	// (e.g. "go-grad Admin"). Required.
	Issuer string
	// PathPrefix is the URL segment (under Config.BasePath) these routes
	// mount beneath, e.g. "security/totp" -> GET {basePath}/security/totp/enroll.
	// Required, leading/trailing slashes trimmed.
	PathPrefix string
	// RequiredRole gates every mounted route exactly like Resource.RequiredRole
	// ("" = any authenticated session -- the expected value for a
	// self-service "manage MY OWN 2FA" feature; a non-empty role additionally
	// requires RoleAuthenticator, the same fail-closed check Register/MountPage
	// already make).
	RequiredRole string
}

// totpEnrollment holds the resolved, validated config the mounted handlers
// close over.
type totpEnrollment struct {
	panel        *Panel
	accountStore auth.AccountStore
	totpStore    auth.TOTPStore
	encKey       []byte
	issuer       string
	prefix       string
	requiredRole string
}

// MountTOTPEnrollment wires the TOTP self-service lifecycle (enroll, QR
// image, confirm, disable, regenerate recovery codes -- 7 routes total,
// disable and regenerate each answering both GET and POST at one Path) onto
// p via MountPage -- the same guard/CSRF/chrome machinery every other page
// in the framework uses. Call once at setup time, alongside Register calls,
// before the first Handler() call.
//
// Every route resolves the acting account from auth.SessionFrom(r.Context())
// -- set by the SAME auth.Require the panel's own guard already runs --
// NEVER from a URL or form parameter, so one operator's session can never
// enroll, inspect, confirm, disable, or rotate recovery codes for another
// account. This requires an Authenticator whose Require populates
// auth.SessionFrom, i.e. *auth.BcryptTOTPAuth: HMACAuth's single-operator
// session carries no account identity, so every route below would fail
// closed with 401 on every request under it -- safe, just not useful.
//
// Panics (fail-closed, setup-time) on: nil Store, a Store not implementing
// auth.TOTPStore, a TOTPEncryptionKey that is not exactly
// auth.TOTPEncryptionKeyLen bytes, an empty Issuer, an empty PathPrefix, or
// an authenticator not implementing SessionCookieName() (CSRF tokens must
// bind to the session cookie -- the same check Register's Writer path makes).
func MountTOTPEnrollment(p *Panel, cfg TOTPEnrollmentConfig) {
	e := newTOTPEnrollment(p, cfg)

	p.MountPage(PageSpec{Path: e.path("enroll"), Handler: e.enrollStart, RequiredRole: e.requiredRole})
	p.MountPage(PageSpec{Path: e.path("qr.png"), Handler: e.qrImage, RequiredRole: e.requiredRole})
	p.MountPage(PageSpec{Path: e.path("confirm"), Handler: e.confirm, RequiredRole: e.requiredRole, Method: http.MethodPost})
	p.MountPage(PageSpec{Path: e.path("disable"), Handler: e.disable, RequiredRole: e.requiredRole})
	p.MountPage(PageSpec{Path: e.path("disable"), Handler: e.disable, RequiredRole: e.requiredRole, Method: http.MethodPost})
	p.MountPage(PageSpec{Path: e.path("regenerate"), Handler: e.regenerate, RequiredRole: e.requiredRole})
	p.MountPage(PageSpec{Path: e.path("regenerate"), Handler: e.regenerate, RequiredRole: e.requiredRole, Method: http.MethodPost})
}

// newTOTPEnrollment validates cfg and returns the resolved handler receiver.
// See MountTOTPEnrollment's doc for the exact panic conditions.
func newTOTPEnrollment(p *Panel, cfg TOTPEnrollmentConfig) *totpEnrollment {
	if cfg.Store == nil {
		panic("resource.MountTOTPEnrollment: Store must not be nil")
	}
	totpStore, ok := cfg.Store.(auth.TOTPStore)
	if !ok {
		panic("resource.MountTOTPEnrollment: Store must also implement auth.TOTPStore")
	}
	if len(cfg.TOTPEncryptionKey) != auth.TOTPEncryptionKeyLen {
		panic(fmt.Sprintf("resource.MountTOTPEnrollment: TOTPEncryptionKey must be exactly %d bytes, got %d", auth.TOTPEncryptionKeyLen, len(cfg.TOTPEncryptionKey)))
	}
	if cfg.Issuer == "" {
		panic("resource.MountTOTPEnrollment: Issuer must not be empty")
	}
	prefix := strings.Trim(cfg.PathPrefix, "/")
	if prefix == "" {
		panic("resource.MountTOTPEnrollment: PathPrefix must not be empty")
	}
	if _, ok := p.auth.(sessionCookier); !ok {
		panic("resource.MountTOTPEnrollment: the authenticator does not implement SessionCookieName() -- CSRF tokens cannot be bound to the session cookie (fail-closed)")
	}
	// CSRFKey is normally required (fail-closed) only once a Writer is
	// registered (validateWriterConfig) -- a Panel with no write Resources
	// can otherwise run with an empty p.csrfKey. This feature is ALWAYS a
	// write surface (Confirm/Disable/Regenerate), so it must make the exact
	// same demand here: an empty or short key turns csrf.Issue/Verify into
	// an HMAC over an empty/weak key -- a PUBLICLY computable function with
	// no secret entropy, letting anyone forge a valid token without ever
	// observing a real one. Same floor and message shape as
	// validateWriterConfig's check, so both fail-closed paths read as one
	// convention, not two.
	if len(p.csrfKey) < minCSRFKeyLen {
		panic(fmt.Sprintf("resource.MountTOTPEnrollment: Config.CSRFKey must be at least %d bytes (got %d) -- this feature's POST routes are CSRF-protected and need a real key regardless of whether any Resource.Writer is registered", minCSRFKeyLen, len(p.csrfKey)))
	}
	return &totpEnrollment{
		panel:        p,
		accountStore: cfg.Store,
		totpStore:    totpStore,
		encKey:       cfg.TOTPEncryptionKey,
		issuer:       cfg.Issuer,
		prefix:       prefix,
		requiredRole: cfg.RequiredRole,
	}
}

// path returns the MountPage-relative path for suffix (under e.prefix).
func (e *totpEnrollment) path(suffix string) string {
	return e.prefix + "/" + suffix
}

// url returns the fully basePath-qualified URL for suffix (under
// e.prefix), for use in rendered href/action/src attributes. Trailing-slash
// terminated to exactly match the pattern MountPage actually registers
// ("..."+suffix+"/{$}") -- net/http's ServeMux 307-redirects a request
// missing that slash before it ever reaches the guard. Browsers transparently
// follow a 307 (method and body preserved, unlike 301/302), so an untrimmed
// link would still "work" end to end, but only via an extra redirect hop on
// every enrollment page load and every Confirm/Disable/Regenerate POST --
// worth avoiding outright rather than relying on client-side redirect
// tolerance for a security-sensitive form submission.
func (e *totpEnrollment) url(suffix string) string {
	return e.panel.basePath + "/" + e.path(suffix) + "/"
}

// currentAccount resolves the acting account from the session on r's
// context (auth.SessionFrom -- set by the SAME Require the mounting guard
// already ran) and re-fetches its current row via accountStore.GetByID, so
// every handler observes live TOTPEnabled/Email state, not a stale session
// claim. Returns ok=false (response already written) when the context
// carries no session -- defense in depth; the guard should make this
// unreachable, but a handler must never fall through to reading a
// zero-value account on that assumption.
func (e *totpEnrollment) currentAccount(w http.ResponseWriter, r *http.Request) (*auth.Account, bool) {
	sess, ok := auth.SessionFrom(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	acct, err := e.accountStore.GetByID(r.Context(), sess.UserID)
	if err != nil {
		slog.ErrorContext(r.Context(), "resource: totp handler could not load session account", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}
	return acct, true
}

// parseForm caps the request body and parses the form, writing a 400 and
// returning false on failure. Mirrors saveHandler's exact convention
// (resource.go) for every write path in this package.
func (e *totpEnrollment) parseForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxTOTPFormBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	return true
}

// render wraps p.RenderPage with the shared error-logging every totp*
// render helper needs, and passes "" for activeID: MountPage does not touch
// nav (by design, see PageSpec's doc), so these pages have no nav item to
// highlight unless the host separately calls AddNav.
func (e *totpEnrollment) render(w http.ResponseWriter, r *http.Request, title string, content templ.Component) {
	if err := e.panel.RenderPage(w, r, title, "", content); err != nil {
		slog.ErrorContext(r.Context(), "resource: render totp page", "title", title, "err", err)
	}
}
