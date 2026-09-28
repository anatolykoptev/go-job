package adminui

// register.go — the P6 PUBLIC self-serve registration surface
// (GET/POST /admin/register), the first unauthenticated write endpoint
// go-job exposes. Mounted on the outer mux beside /admin/login, ONLY under
// the bcrypt driver (d.selfServe) — under AUTH_DRIVER=hmac the route does
// not exist and the panel catch-all answers 404/login-redirect.
//
// Defense stack, in POST order (cheap checks before bcrypt):
//  1. body cap + ParseForm
//  2. honeypot ("website") — bots filling it get the identical success page,
//     no insert (their reward is indistinguishable from success)
//  3. anonymous-CSRF: gj_reg_nonce cookie + csrf.Issue/Verify bound to it —
//     the session-cookie binding MountAction uses is unavailable (anonymous
//     requests have no session cookie; binding to "" would be forgeable)
//  4. dual fixed-window LoginLimiter: reg:min:<ip> 5/min (burst) AND
//     reg:hour:<ip> 20/h (sustained-abuse — 5/min alone admits 300/hr of
//     junk pending rows)
//  5. field validation (single mail.ParseAddress address, ≤254 chars,
//     password 10..72 bytes — the bcrypt truncation boundary, name ≤128)
//  6. auth.HashPassword THEN accounts.RegisterPending — hashing runs before
//     the conflict check either way, so bcrypt cost equalizes the
//     taken/untaken timing; created true/false render the IDENTICAL success
//     page (no enumeration oracle). A DB error is a generic 500.
//
// The page is standalone (no session/nav → no RenderPage), links pm7.css,
// and carries inline <style> only — shell.SecurityHeaders' CSP forbids
// inline scripts, so the page ships zero JavaScript.

import (
	"crypto/rand"
	"encoding/hex"
	"html/template"
	"log/slog"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/anatolykoptev/go-panel/auth"
	"github.com/anatolykoptev/go-panel/csrf"
	"github.com/anatolykoptev/go-panel/shell"
	"github.com/anatolykoptev/go_job/internal/accounts"
	"github.com/jackc/pgx/v5/pgxpool"
)

// regNonceCookie binds the anonymous CSRF token to a cookie the cross-origin
// attacker cannot read (HttpOnly) and browsers won't attach to cross-site
// POSTs (SameSite=Strict) — verification fails closed on either axis.
const regNonceCookie = "gj_reg_nonce"

const (
	// regMinLimit/regHourLimit are the two windows of the register throttle.
	regMinLimit   = 5
	regMinWindow  = time.Minute
	regHourLimit  = 20
	regHourWindow = time.Hour

	regMaxEmailLen    = 254
	regMinPasswordLen = 10
	regMaxPasswordLen = 72 // bcrypt's truncation boundary — HashPassword must never see more
	regMaxNameLen     = 128
)

// registerHandler serves GET/POST /admin/register.
type registerHandler struct {
	pool    *pgxpool.Pool
	csrfKey []byte
	limiter *accounts.LoginLimiter // dedicated instance — not login's
}

func newRegisterHandler(pool *pgxpool.Pool, csrfKey []byte) *registerHandler {
	return &registerHandler{pool: pool, csrfKey: csrfKey, limiter: accounts.NewLoginLimiter()}
}

// get renders the registration form. EVERY GET mints a fresh nonce — an
// inbound gj_reg_nonce cookie is deliberately ignored so a stale or
// attacker-planted cookie can never pair with a live token.
func (h *registerHandler) get(w http.ResponseWriter, r *http.Request) {
	shell.SecurityHeaders(w)
	h.renderForm(w, r, "", http.StatusOK)
}

// post runs the cheap-first pipeline documented in the file header.
func (h *registerHandler) post(w http.ResponseWriter, r *http.Request) {
	shell.SecurityHeaders(w)
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Honeypot: a filled "website" field means a bot (humans can't see it).
	// Answer the IDENTICAL success page — confirming failure to a bot only
	// teaches it to retry.
	if r.FormValue("website") != "" {
		slog.Warn("adminui: register honeypot tripped",
			slog.String("key", "reg:honeypot"), slog.String("ip", proxiedClientIP(r)))
		h.renderSuccess(w)
		return
	}

	// Anonymous CSRF: the token must verify against the nonce cookie.
	nonceCookie, err := r.Cookie(regNonceCookie)
	if err != nil || nonceCookie.Value == "" ||
		csrf.Verify(h.csrfKey, nonceCookie.Value, r.FormValue(csrf.FormField)) != nil {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Dual-window rate limit: burst (5/min) AND sustained (20/h), both
	// fail-closed — a limiter error denies rather than allowing.
	ip := proxiedClientIP(r)
	if !h.allow(w, r, "reg:min:"+ip, regMinLimit, regMinWindow) ||
		!h.allow(w, r, "reg:hour:"+ip, regHourLimit, regHourWindow) {
		return
	}

	email, name, password, fieldErr := validateRegisterForm(r)
	if fieldErr != "" {
		h.renderForm(w, r, fieldErr, http.StatusOK)
		return
	}

	// Hash BEFORE the conflict-visibility point regardless of outcome — the
	// bcrypt cost is what makes created-vs-conflict indistinguishable.
	hash, err := auth.HashPassword(password)
	if err != nil {
		slog.Error("adminui: register hash", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if _, err := accounts.RegisterPending(r.Context(), h.pool, email, name, hash); err != nil {
		slog.Error("adminui: register insert", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// created true/false → byte-identical success page: the response never
	// reveals whether the email was already registered.
	h.renderSuccess(w)
}

// allow checks one limiter window; on deny it writes 429 + Retry-After
// (sized to the denied window) and re-renders the form with a generic line.
// Retry-After must be set before renderForm writes the status line.
func (h *registerHandler) allow(w http.ResponseWriter, r *http.Request, key string, limit int, window time.Duration) bool {
	allowed, err := h.limiter.Allow(r.Context(), key, limit, window)
	if err != nil {
		slog.Error("adminui: register limiter error — denying (fail-closed)", "err", err)
		allowed = false
	}
	if allowed {
		return true
	}
	w.Header().Set("Retry-After", strconv.Itoa(max(1, int(window.Seconds()))))
	h.renderForm(w, r, "Too many attempts. Please try again later.", http.StatusTooManyRequests)
	return false
}

// validateRegisterForm returns (normalized email, name, password, "") on
// success or ("", "", "", fieldMessage) on failure. Messages are field-level
// and carry no account-state information.
func validateRegisterForm(r *http.Request) (string, string, string, string) {
	email := strings.TrimSpace(r.FormValue("email"))
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || len(email) > regMaxEmailLen {
		return "", "", "", "Enter a valid email address."
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if len(name) > regMaxNameLen {
		return "", "", "", "Name is too long (max 128 characters)."
	}
	password := r.FormValue("password")
	if len(password) < regMinPasswordLen {
		return "", "", "", "Password must be at least 10 characters."
	}
	if len(password) > regMaxPasswordLen {
		return "", "", "", "Password must be at most 72 bytes."
	}
	return strings.ToLower(email), name, password, ""
}

// renderForm renders the standalone registration card with a fresh nonce
// cookie + bound CSRF token, then writes status (use http.StatusOK for the
// normal 200 — WriteHeader is skipped so Go's implicit 200 stands).
// errMsg is rendered in the alert slot ("" = none).
func (h *registerHandler) renderForm(w http.ResponseWriter, r *http.Request, errMsg string, status int) {
	nonce := newNonce()
	// Cookie Max-Age (1h) is shorter than the token TTL (csrf.DefaultTTL 2h) —
	// benign fail-closed: an expired cookie denies, never admits.
	//nolint:gosec // nonce is a CSRF-binding cookie, not a session credential.
	http.SetCookie(w, &http.Cookie{
		Name:     regNonceCookie,
		Value:    nonce,
		Path:     adminBasePath + "/register",
		MaxAge:   3600,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	data := registerPageData{
		BasePath: adminBasePath,
		ErrMsg:   errMsg,
		CSRF:     csrf.Issue(h.csrfKey, nonce, csrf.DefaultTTL),
	}
	if err := registerPageTmpl.Execute(w, data); err != nil {
		slog.Error("adminui: render register page", "err", err)
	}
}

// renderSuccess writes the generic success page — IDENTICAL bytes whether the
// row was created or the email was already registered.
func (h *registerHandler) renderSuccess(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := registerSuccessTmpl.Execute(w, registerPageData{BasePath: adminBasePath}); err != nil {
		slog.Error("adminui: render register success", "err", err)
	}
}

// newNonce returns 32 random bytes hex-encoded for the CSRF-binding cookie.
// On crypto/rand failure it returns "" — the issued token then binds to the
// empty value, which a cross-site attacker still cannot mint (Issue requires
// the server key), so the failure degrades rather than opens.
func newNonce() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		slog.Error("adminui: crypto/rand failed for register nonce", "err", err)
		return ""
	}
	return hex.EncodeToString(b[:])
}

// registerPageData feeds registerPageTmpl / registerSuccessTmpl.
type registerPageData struct {
	BasePath string
	ErrMsg   string
	CSRF     string
}

// The standalone-card idiom mirrors shell.LoginPage's layout (pm7 tokens +
// inline style, no JS — CSP forbids inline scripts here).
var registerPageTmpl = template.Must(template.New("register").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>Request access — go-job</title>
<link rel="stylesheet" href="{{.BasePath}}/static/pm7.css"/>
<style>
:root{
	--bg-deep:#060b18;--bg-surface:#131c31;--accent:#3b82f6;
	--accent-glow:rgba(59,130,246,.15);--text-primary:#e8edf5;
	--text-secondary:#7b8ba8;--border:#1e2d4a;
	--font-sans:system-ui,-apple-system,'Segoe UI',Roboto,sans-serif;
}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;background:var(--bg-deep);color:var(--text-primary);font-family:var(--font-sans);font-size:.875rem;padding:1.5rem}
.reg-shell{width:100%;max-width:22rem}
.reg-card{width:100%;padding:2rem;background:var(--bg-surface);border:1px solid var(--border);border-radius:.75rem}
.reg-title{margin:0 0 1.5rem;font-size:1.125rem;font-weight:700;color:var(--text-primary);text-align:center;letter-spacing:.01em}
.reg-error{margin-bottom:1rem;padding:.5rem .75rem;border-radius:.375rem;background:rgba(239,68,68,.12);border:1px solid rgba(239,68,68,.35);color:#fca5a5;font-size:.8125rem;text-align:center}
.reg-field{margin-bottom:1rem}
.reg-field label{display:block;margin-bottom:.375rem;font-size:.8125rem;color:var(--text-secondary)}
.reg-form .pm7-input{width:100%;padding:.5rem .75rem;background:var(--bg-deep);border:1px solid var(--border);border-radius:.5rem;color:var(--text-primary);font-size:.875rem;outline:none;transition:border-color .15s,box-shadow .15s}
.reg-form .pm7-input:focus{border-color:var(--accent);box-shadow:0 0 0 3px var(--accent-glow)}
.reg-submit{width:100%;margin-top:.25rem;padding:.625rem;background:var(--accent);color:#fff;border:none;border-radius:.5rem;font-size:.875rem;font-weight:600;cursor:pointer;transition:background-color .15s}
.reg-submit:hover{background:#2563eb}
.reg-links{margin-top:1rem;text-align:center}
.reg-links a{color:var(--accent);font-size:.8125rem;text-decoration:none}
.reg-links a:hover{text-decoration:underline}
.reg-hp{position:absolute;left:-9999px;top:-9999px;height:0;width:0;overflow:hidden}
</style>
</head>
<body>
<main class="reg-shell">
<div class="pm7-card reg-card">
<div class="pm7-card-content">
<h1 class="reg-title">Request access</h1>
{{if .ErrMsg}}<div class="reg-error" role="alert">{{.ErrMsg}}</div>{{end}}
<form method="POST" action="{{.BasePath}}/register" class="reg-form">
<input type="hidden" name="_csrf" value="{{.CSRF}}"/>
<div class="reg-field">
<label for="email">Email</label>
<input id="email" name="email" type="email" autocomplete="email" required autofocus class="pm7-input"/>
</div>
<div class="reg-field">
<label for="name">Name</label>
<input id="name" name="name" type="text" autocomplete="name" maxlength="128" class="pm7-input"/>
</div>
<div class="reg-field">
<label for="password">Password</label>
<input id="password" name="password" type="password" autocomplete="new-password" minlength="10" required class="pm7-input"/>
</div>
<div class="reg-hp" aria-hidden="true">
<label>Website <input type="text" name="website" tabindex="-1" autocomplete="off"/></label>
</div>
<button type="submit" class="pm7-button pm7-button--primary reg-submit">Register</button>
</form>
<nav class="reg-links"><a href="{{.BasePath}}/login">Already registered? Sign in</a></nav>
</div>
</div>
</main>
</body>
</html>`))

var registerSuccessTmpl = template.Must(template.New("register-ok").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>Request access — go-job</title>
<link rel="stylesheet" href="{{.BasePath}}/static/pm7.css"/>
<style>
:root{--bg-deep:#060b18;--bg-surface:#131c31;--accent:#3b82f6;--text-primary:#e8edf5;--text-secondary:#7b8ba8;--border:#1e2d4a;--font-sans:system-ui,-apple-system,'Segoe UI',Roboto,sans-serif}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;background:var(--bg-deep);color:var(--text-primary);font-family:var(--font-sans);font-size:.875rem;padding:1.5rem}
.reg-shell{width:100%;max-width:22rem}
.reg-card{width:100%;padding:2rem;background:var(--bg-surface);border:1px solid var(--border);border-radius:.75rem;text-align:center}
.reg-title{margin:0 0 1rem;font-size:1.125rem;font-weight:700;color:var(--text-primary)}
.reg-note{color:var(--text-secondary);font-size:.875rem;line-height:1.5}
.reg-links{margin-top:1.5rem}
.reg-links a{color:var(--accent);font-size:.8125rem;text-decoration:none}
.reg-links a:hover{text-decoration:underline}
</style>
</head>
<body>
<main class="reg-shell">
<div class="pm7-card reg-card">
<div class="pm7-card-content">
<h1 class="reg-title">Registration received</h1>
<p class="reg-note">The account signs in after an operator activates it.</p>
<nav class="reg-links"><a href="{{.BasePath}}/login">Back to sign in</a></nav>
</div>
</div>
</main>
</body>
</html>`))
