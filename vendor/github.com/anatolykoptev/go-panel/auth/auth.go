// Package auth provides the admin session authentication abstraction for go-panel.
//
// Two implementations ship:
//   - HMACAuth — single-user HMAC cookie (ported from go-nerv/internal/admin/auth.go).
//     Ships in foundations; use for single-operator setups.
//   - BcryptTOTPAuth — multi-user bcrypt + TOTP + Redis login RL (ported from
//     oxpulse-admin). Stubbed behind the same Authenticator interface; wire in
//     Phase 2 when a second editor is needed.
//
// # Session cookie format (v2)
//
//	exp.nonce.HMAC-SHA256(key, username|exp|nonce)
//
// The nonce is 16 bytes of crypto/rand, hex-encoded (32 chars). It changes on
// every login, so CSRF tokens bound to a prior session value are invalidated the
// moment the user logs in again. Logout + re-login = full CSRF token rotation.
//
// # Breaking change from v1
//
// The old 2-part format "exp.HMAC" is rejected by Verified. Active sessions
// from a v1 deployment are invalidated on first deploy of v2. Operators will
// need to log in again.
//
// Usage:
//
//	a := auth.NewHMACAuth(auth.HMACConfig{
//	    Username:  os.Getenv("ADMIN_USER"),
//	    Password:  os.Getenv("ADMIN_PASSWORD"),
//	    HMACKey:   []byte(os.Getenv("ADMIN_HMAC_KEY")),
//	    BasePath:  "/admin",
//	    SessionTTL: 24 * time.Hour,
//	})
//	mux.Handle("/admin/login", a.LoginHandler())
//	mux.Handle("/admin/logout", a.LogoutHandler())
//	// Protect a route:
//	mux.HandleFunc("/admin/", a.Require(myHandler))
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultSessionCookie = "panel_admin"
	defaultSessionTTL    = 24 * time.Hour
)

// Authenticator is the pluggable session-auth interface.
// Implementations: HMACAuth (foundations), BcryptTOTPAuth (Phase 2).
type Authenticator interface {
	// Verified reports whether the current request has a valid session.
	Verified(r *http.Request) bool
	// LoginHandler serves GET+POST /admin/login.
	LoginHandler() http.Handler
	// LogoutHandler serves GET /admin/logout.
	LogoutHandler() http.Handler
	// Require wraps next: if session invalid → redirect or 401 (htmx).
	Require(next http.HandlerFunc) http.HandlerFunc
}

// HMACConfig configures the HMAC single-user authenticator.
type HMACConfig struct {
	// Username + Password are the single operator credentials.
	Username string
	Password string
	// HMACKey is the signing key for session cookies.
	HMACKey []byte
	// BasePath is the admin prefix, e.g. "/admin". Used for cookie Path and redirects.
	BasePath string
	// CookieName overrides the default cookie name. Empty → "panel_admin".
	CookieName string
	// SessionTTL is the cookie/session lifetime. Zero → 24h.
	SessionTTL time.Duration
	// Secure controls the Secure cookie flag. Set false only in local dev (http).
	Secure bool
	// LoginTempl is a templ component function for the login page.
	// When nil, a minimal built-in HTML form is used.
	LoginTempl func(errMsg string) http.Handler
}

// HMACAuth is a single-user HMAC-cookie session authenticator.
// Ported from go-nerv/internal/admin/auth.go.
type HMACAuth struct {
	cfg        HMACConfig
	cookieName string
	sessionTTL time.Duration
	basePath   string
}

// minHMACKeyLen is the minimum acceptable HMAC key length.
// Matches the RFC 2104 recommendation and the CSRFKey floor in resource/resource.go.
const minHMACKeyLen = 32

// NewHMACAuth creates a configured HMACAuth. Panics on short/empty HMACKey or Username.
func NewHMACAuth(cfg HMACConfig) *HMACAuth {
	if len(cfg.HMACKey) < minHMACKeyLen {
		panic(fmt.Sprintf("auth.NewHMACAuth: HMACKey must be at least %d bytes, got %d (RFC 2104 key-length floor)", minHMACKeyLen, len(cfg.HMACKey)))
	}
	if cfg.Username == "" {
		panic("auth.NewHMACAuth: Username must not be empty")
	}
	name := cfg.CookieName
	if name == "" {
		name = defaultSessionCookie
	}
	ttl := cfg.SessionTTL
	if ttl == 0 {
		ttl = defaultSessionTTL
	}
	bp := cfg.BasePath
	if bp == "" {
		bp = "/admin"
	}
	return &HMACAuth{cfg: cfg, cookieName: name, sessionTTL: ttl, basePath: bp}
}

// SessionCookieName returns the cookie name used for session tokens.
// Implements resource.sessionCookier for CSRF double-submit binding.
func (a *HMACAuth) SessionCookieName() string {
	return a.cookieName
}

// Verified reports whether the request has a valid, non-expired HMAC session cookie.
//
// Cookie format (v2, introduced with per-login nonce):
//
//	exp.nonce.HMAC-SHA256(key, username|exp|nonce)
//
// All three parts are required. The legacy 2-part format (exp.HMAC) is explicitly
// rejected so that old sessions are invalidated after a deploy of this version.
func (a *HMACAuth) Verified(r *http.Request) bool {
	c, err := r.Cookie(a.cookieName)
	if err != nil {
		return false
	}
	// Expect exactly 2 dots → 3 parts: exp, nonce, sig.
	const cookieParts = 3
	parts := strings.SplitN(c.Value, ".", cookieParts)
	if len(parts) != cookieParts {
		return false
	}
	expStr, nonce, sig := parts[0], parts[1], parts[2]
	if nonce == "" || sig == "" {
		return false
	}
	exp, err2 := strconv.ParseInt(expStr, 10, 64)
	if err2 != nil || exp == 0 || time.Now().Unix() > exp {
		return false
	}
	mac := hmac.New(sha256.New, a.cfg.HMACKey)
	_, _ = fmt.Fprintf(mac, "%s|%d|%s", a.cfg.Username, exp, nonce)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(sig))
}

// makeToken generates a new session token with a per-login random nonce.
// Returns ("", error) on crypto/rand failure — callers must treat this as fatal.
func (a *HMACAuth) makeToken() (string, error) {
	exp := time.Now().Add(a.sessionTTL).Unix()
	var nb [16]byte
	if _, err := rand.Read(nb[:]); err != nil {
		return "", fmt.Errorf("auth: crypto/rand failed: %w", err)
	}
	nonce := hex.EncodeToString(nb[:])
	mac := hmac.New(sha256.New, a.cfg.HMACKey)
	_, _ = fmt.Fprintf(mac, "%s|%d|%s", a.cfg.Username, exp, nonce)
	sig := hex.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("%d.%s.%s", exp, nonce, sig), nil
}

// LoginHandler returns an http.Handler for GET + POST /admin/login.
func (a *HMACAuth) LoginHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method == http.MethodPost {
			const maxLoginBodyBytes = 4096 // 4KB is plenty for a login form
			r.Body = http.MaxBytesReader(w, r.Body, maxLoginBodyBytes)
			_ = r.ParseForm()
			// Constant-time on both fields: a byte-wise == short-circuits on the
			// first mismatch and leaks credential prefixes through timing.
			userOK := subtle.ConstantTimeCompare([]byte(r.FormValue("username")), []byte(a.cfg.Username)) == 1
			passOK := subtle.ConstantTimeCompare([]byte(r.FormValue("password")), []byte(a.cfg.Password)) == 1
			if userOK && passOK {
				tok, err := a.makeToken()
				if err != nil {
					slog.Error("auth: failed to generate session token", "err", err)
					http.Error(w, "internal server error", http.StatusInternalServerError)
					return
				}
				http.SetCookie(w, &http.Cookie{
					Name:     a.cookieName,
					Value:    tok,
					Path:     a.basePath,
					MaxAge:   int(a.sessionTTL.Seconds()),
					HttpOnly: true,
					Secure:   a.cfg.Secure,
					SameSite: http.SameSiteLaxMode,
				})
				http.Redirect(w, r, a.basePath+"/", http.StatusSeeOther)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			a.renderLogin(w, "Invalid username or password")
			return
		}
		if a.Verified(r) {
			http.Redirect(w, r, a.basePath+"/", http.StatusSeeOther)
			return
		}
		a.renderLogin(w, "")
	})
}

// LogoutHandler returns an http.Handler for GET /admin/logout.
func (a *HMACAuth) LogoutHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: a.cookieName, Path: a.basePath, MaxAge: -1})
		http.Redirect(w, r, a.basePath+"/login", http.StatusSeeOther)
	})
}

// Require wraps next: redirects unauthenticated requests to /admin/login.
// For htmx requests it returns 401 + HX-Redirect instead of a full redirect.
func (a *HMACAuth) Require(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.Verified(r) {
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", a.basePath+"/login")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, a.basePath+"/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// renderLogin writes the login form HTML. Uses LoginTempl if provided,
// otherwise falls back to the built-in minimal form.
func (a *HMACAuth) renderLogin(w http.ResponseWriter, errMsg string) {
	if a.cfg.LoginTempl != nil {
		a.cfg.LoginTempl(errMsg).ServeHTTP(w, nil) //nolint:staticcheck // nil r is intentional for template-only render
		return
	}
	writeBuiltinLoginPage(w, a.cfg.BasePath, errMsg)
}

// writeBuiltinLoginPage writes a minimal login HTML form when no templ template
// is configured. Intended for testing and quick-start setups.
func writeBuiltinLoginPage(w http.ResponseWriter, basePath, errMsg string) {
	errSection := ""
	if errMsg != "" {
		errSection = `<p style="color:#ef4444">` + errMsg + `</p>`
	}
	html := `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Admin Login</title></head>
<body style="min-height:100vh;display:flex;align-items:center;justify-content:center;background:#0f172a;color:#e2e8f0;font-family:system-ui">
<div style="background:#1e293b;padding:2rem;border-radius:.75rem;width:100%;max-width:22rem">
<h1 style="font-size:1.25rem;font-weight:600;margin-bottom:1.5rem">Admin</h1>
` + errSection + `
<form method="POST" action="` + basePath + `/login">
<label style="display:block;margin-bottom:.75rem">
  <span style="font-size:.75rem;color:#94a3b8">Username</span><br>
  <input name="username" type="text" required autofocus
    style="width:100%;padding:.5rem .75rem;background:#0f172a;border:1px solid #334155;border-radius:.375rem;color:#e2e8f0;margin-top:.25rem">
</label>
<label style="display:block;margin-bottom:1rem">
  <span style="font-size:.75rem;color:#94a3b8">Password</span><br>
  <input name="password" type="password" required
    style="width:100%;padding:.5rem .75rem;background:#0f172a;border:1px solid #334155;border-radius:.375rem;color:#e2e8f0;margin-top:.25rem">
</label>
<button type="submit"
  style="width:100%;padding:.625rem;background:#3b82f6;color:#fff;border:none;border-radius:.375rem;cursor:pointer;font-size:.875rem">
  Sign in
</button>
</form></div></body></html>`
	_, _ = w.Write([]byte(html))
}
