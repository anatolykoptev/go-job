package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/anatolykoptev/go-panel/shell"
)

// BcryptTOTPAuth is a multi-user, bcrypt-password session authenticator backed by
// an AccountStore. It implements Authenticator. TOTP second-factor and login
// rate-limiting are planned and not yet wired; the type name reflects the target
// shape. Ported in design from oxpulse-admin/internal/admin/auth.go.
type BcryptTOTPAuth struct {
	store      AccountStore
	hmacKey    []byte
	basePath   string
	cookieName string
	sessionTTL time.Duration
	secure     bool
	loginTempl func(errMsg string) http.Handler
}

// BcryptConfig configures BcryptTOTPAuth.
type BcryptConfig struct {
	// Store is the account persistence seam (required).
	Store AccountStore
	// HMACKey signs session cookies (required, >= 32 bytes).
	HMACKey []byte
	// BasePath is the admin prefix; default "/admin".
	BasePath string
	// CookieName overrides the session cookie name; default "panel_admin".
	CookieName string
	// SessionTTL is the session lifetime; default 24h.
	SessionTTL time.Duration
	// Secure sets the Secure cookie flag (false only for local-dev http).
	Secure bool
	// LoginTempl optionally overrides the login page.
	LoginTempl func(errMsg string) http.Handler
}

// NewBcryptTOTPAuth validates cfg and returns a BcryptTOTPAuth. Panics on a nil
// Store or a short/empty HMACKey (fail-closed configuration).
func NewBcryptTOTPAuth(cfg BcryptConfig) *BcryptTOTPAuth {
	if cfg.Store == nil {
		panic("auth.NewBcryptTOTPAuth: Store must not be nil")
	}
	if len(cfg.HMACKey) < minHMACKeyLen {
		panic(fmt.Sprintf("auth.NewBcryptTOTPAuth: HMACKey must be at least %d bytes, got %d", minHMACKeyLen, len(cfg.HMACKey)))
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
	return &BcryptTOTPAuth{
		store:      cfg.Store,
		hmacKey:    cfg.HMACKey,
		basePath:   bp,
		cookieName: name,
		sessionTTL: ttl,
		secure:     cfg.Secure,
		loginTempl: cfg.LoginTempl,
	}
}

var _ Authenticator = (*BcryptTOTPAuth)(nil)

// RoleOwner is the super-role that RequireRole always permits.
const RoleOwner = "owner"

// dummyPasswordHash equalizes login timing: on the unknown/inactive-email path
// we compare against it so a non-existent account costs the same bcrypt work as
// a wrong password, closing the user-enumeration timing oracle. Computed once.
var dummyPasswordHash = mustDummyHash()

func mustDummyHash() string {
	h, err := HashPassword("timing-equalizer-not-a-real-password")
	if err != nil {
		panic("auth: failed to compute dummy password hash: " + err.Error())
	}
	return h
}

// SessionCookieName implements resource.sessionCookier for CSRF double-submit binding.
func (a *BcryptTOTPAuth) SessionCookieName() string { return a.cookieName }

// sessionData is the JSON payload of a bcrypt session token.
type sessionData struct {
	UserID string `json:"uid"`
	Role   string `json:"role"`
	Exp    int64  `json:"exp"`
	Nonce  string `json:"n"`
}

// Session is the authenticated session exposed to downstream handlers via SessionFrom.
type Session struct {
	UserID string
	Role   string
}

type sessionCtxKey struct{}

// SessionFrom returns the Session stored on the context by Require, if present.
func SessionFrom(ctx context.Context) (*Session, bool) {
	s, ok := ctx.Value(sessionCtxKey{}).(*Session)
	return s, ok
}

// makeToken builds a signed session token: base64url(json) + "." + HMAC-SHA256.
// A per-login random nonce rotates the cookie value (and thus the CSRF binding).
func (a *BcryptTOTPAuth) makeToken(userID, role string) (string, error) {
	var nb [16]byte
	if _, err := rand.Read(nb[:]); err != nil {
		return "", fmt.Errorf("auth: crypto/rand failed: %w", err)
	}
	sd := sessionData{
		UserID: userID,
		Role:   role,
		Exp:    time.Now().Add(a.sessionTTL).Unix(),
		Nonce:  hex.EncodeToString(nb[:]),
	}
	payload, err := json.Marshal(sd)
	if err != nil {
		return "", fmt.Errorf("auth: marshal session: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, a.hmacKey)
	_, _ = mac.Write([]byte(encoded))
	sig := hex.EncodeToString(mac.Sum(nil))
	return encoded + "." + sig, nil
}

// parseToken verifies the token MAC and expiry, returning the decoded session.
func (a *BcryptTOTPAuth) parseToken(value string) (*sessionData, bool) {
	dot := strings.LastIndex(value, ".")
	if dot < 0 {
		return nil, false
	}
	encoded, sig := value[:dot], value[dot+1:]
	if encoded == "" || sig == "" {
		return nil, false
	}
	mac := hmac.New(sha256.New, a.hmacKey)
	_, _ = mac.Write([]byte(encoded))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, false
	}
	var sd sessionData
	if err := json.Unmarshal(payload, &sd); err != nil {
		return nil, false
	}
	if sd.Exp == 0 || time.Now().Unix() > sd.Exp {
		return nil, false
	}
	return &sd, true
}

// Verified implements Authenticator: a fast crypto-only session check (no DB).
func (a *BcryptTOTPAuth) Verified(r *http.Request) bool {
	c, err := r.Cookie(a.cookieName)
	if err != nil {
		return false
	}
	_, ok := a.parseToken(c.Value)
	return ok
}

func (a *BcryptTOTPAuth) renderLogin(ctx context.Context, w http.ResponseWriter, errMsg string) {
	if a.loginTempl != nil {
		a.loginTempl(errMsg).ServeHTTP(w, nil) //nolint:staticcheck // nil r is intentional for template-only render
		return
	}
	ident := shell.LoginIdentifier{Label: "Email", Name: "email", Type: "email", Autocomplete: "email"}
	if err := shell.LoginPage(a.basePath, ident, errMsg).Render(ctx, w); err != nil {
		slog.Error("auth: failed to render login page", "err", err)
	}
}

// LoginHandler implements Authenticator: GET renders the form, POST checks
// email + bcrypt password and issues a session cookie.
func (a *BcryptTOTPAuth) LoginHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method != http.MethodPost {
			if a.Verified(r) {
				http.Redirect(w, r, a.basePath+"/", http.StatusSeeOther)
				return
			}
			a.renderLogin(r.Context(), w, "")
			return
		}
		const maxLoginBodyBytes = 4096
		r.Body = http.MaxBytesReader(w, r.Body, maxLoginBodyBytes)
		_ = r.ParseForm()
		email := r.FormValue("email")
		password := r.FormValue("password")

		// Generic error on both unknown-user and bad-password to avoid user enumeration.
		acct, err := a.store.GetByEmail(r.Context(), email)
		if err != nil {
			// Equalize timing with the verify path: an unknown/inactive email must
			// cost the same bcrypt work as a wrong password (no enumeration oracle).
			_ = VerifyPassword(password, dummyPasswordHash)
			w.WriteHeader(http.StatusUnauthorized)
			a.renderLogin(r.Context(), w, "Invalid email or password")
			return
		}
		if !VerifyPassword(password, acct.PasswordHash) {
			w.WriteHeader(http.StatusUnauthorized)
			a.renderLogin(r.Context(), w, "Invalid email or password")
			return
		}
		tok, err := a.makeToken(acct.ID, acct.Role)
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
			Secure:   a.secure,
			SameSite: http.SameSiteLaxMode,
		})
		if err := a.store.UpdateLastLogin(r.Context(), acct.ID); err != nil {
			slog.Warn("auth: update last login failed", "err", err)
		}
		http.Redirect(w, r, a.basePath+"/", http.StatusSeeOther)
	})
}

// LogoutHandler implements Authenticator.
func (a *BcryptTOTPAuth) LogoutHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: a.cookieName, Path: a.basePath, MaxAge: -1})
		http.Redirect(w, r, a.basePath+"/login", http.StatusSeeOther)
	})
}

// Require implements Authenticator: validates the session token AND re-checks the
// account against the store, so a deactivated / deleted / role-changed account
// loses access on the next request (instant revocation). Fail-open on transient
// DB errors (the crypto token is valid); fail-closed on not-found / inactive / role drift.
func (a *BcryptTOTPAuth) Require(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sd := a.liveSession(r)
		if sd == nil {
			a.reject(w, r)
			return
		}
		ctx := context.WithValue(r.Context(), sessionCtxKey{}, &Session{UserID: sd.UserID, Role: sd.Role})
		next(w, r.WithContext(ctx))
	}
}

// liveSession returns the validated + revocation-checked session, or nil.
func (a *BcryptTOTPAuth) liveSession(r *http.Request) *sessionData {
	c, err := r.Cookie(a.cookieName)
	if err != nil {
		return nil
	}
	sd, ok := a.parseToken(c.Value)
	if !ok {
		return nil
	}
	acct, err := a.store.GetByID(r.Context(), sd.UserID)
	if err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			return nil // account deleted -> revoke
		}
		slog.Warn("auth: session recheck DB error — allowing crypto-valid token", "err", err)
		return sd // fail open on transient DB error
	}
	if !acct.Active || acct.Role != sd.Role {
		return nil // deactivated or role changed -> revoke
	}
	return sd
}

func (a *BcryptTOTPAuth) reject(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", a.basePath+"/login")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, a.basePath+"/login", http.StatusSeeOther)
}

// RequireRole wraps Require and additionally requires the session role to equal
// role (or "owner", which is always permitted). Not part of Authenticator.
func (a *BcryptTOTPAuth) RequireRole(role string, next http.HandlerFunc) http.HandlerFunc {
	return a.Require(func(w http.ResponseWriter, r *http.Request) {
		s, ok := SessionFrom(r.Context())
		if !ok || (s.Role != role && s.Role != RoleOwner) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	})
}
