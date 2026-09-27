package adminui

// Auth-driver selection for the operator admin (plan ADR-2/ADR-17).
//
// AUTH_DRIVER selects the driver; anything other than "hmac" (including unset)
// is bcrypt+TOTP — the multi-account path. "hmac" is the explicit
// single-operator rollback lever:
//
//	bcrypt (default) — per-account bcrypt+TOTP sessions over panel_accounts
//	  (auth.BcryptTOTPAuth). Tenant identity is the session account UUID,
//	  resolved per request through SessionFromRequest (sessionTenantResolver +
//	  accountMatchAuthorizer, plan ADR-5's go-job half).
//	hmac — full single-operator mode (ADR-17): the HMAC cookie gate only; it
//	  never stamps an auth.Session, so account identity CANNOT come from the
//	  request — every authenticated request resolves to the static operator
//	  tenant pin (operator account UUID, or accounts.SingleOperatorSlug when no
//	  account store exists). Consequence: no resource in this package may set
//	  RequiredRole — HMACAuth is not a RoleAuthenticator and
//	  resource.Register panics fail-closed (the zero-RequiredRole fitness gate
//	  in Makefile preflight asserts it stays so).

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/anatolykoptev/go-panel/auth"
	"github.com/anatolykoptev/go-panel/tenant"
	"github.com/anatolykoptev/go_job/internal/accounts"
)

const (
	// authDriverHMAC is the AUTH_DRIVER value selecting the single-operator
	// HMAC rollback path (ADR-17).
	authDriverHMAC = "hmac"

	// loginRate / totpRate bound the bcrypt login + TOTP verification attempts
	// per client IP (per-IP because login is unauthenticated — the only key
	// available before identity exists). Deliberately tight: an operator types
	// credentials a handful of times per day; 5/min and 10/min never bite a
	// human but kill credential/TOTP stuffing.
	loginRateLimit  = 5
	loginRateWindow = time.Minute
	totpRateLimit   = 10
	totpRateWindow  = time.Minute

	// totpIssuer is the label operator authenticator apps show for enrolled
	// TOTP secrets.
	totpIssuer = "go-job"
)

// driver bundles the authenticator plus the tenant Resolver/Authorizer pair
// resource.Config consumes — the pair is driver-dependent: bcrypt resolves the
// session account per request (ADR-5), hmac pins the static operator slug
// (ADR-17).
type driver struct {
	authn      auth.Authenticator
	resolver   tenant.Resolver
	authorizer tenant.Authorizer
	totpKey    []byte // non-nil only under bcrypt — feeds MountTOTPEnrollment
}

// selectDriver builds the configured auth driver. Returns ok=false when the
// selected driver's env/store requirements are unmet — admin disabled, never
// silently half-configured.
func selectDriver(acctStore *auth.PgxAccountStore, operatorID, hmacKey, password, adminUser string) (*driver, bool) {
	if os.Getenv("AUTH_DRIVER") == authDriverHMAC {
		slog.Warn("adminui: AUTH_DRIVER=hmac — single-operator rollback mode (ADR-17); bcrypt+TOTP sessions and per-account identity are OFF")
		return hmacDriver(hmacKey, password, adminUser, operatorID), true
	}
	return bcryptDriver(acctStore, hmacKey)
}

// bcryptDriver builds the default multi-account driver. Requires the account
// store (Bootstrap must have run — fail-closed: no store, no admin) and
// ADMIN_TOTP_ENC_KEY for secret-at-rest encryption (independent of the session
// HMAC key — see BcryptConfig.TOTPEncryptionKey: rotating one must never
// destroy the other).
func bcryptDriver(acctStore *auth.PgxAccountStore, hmacKey string) (*driver, bool) {
	if acctStore == nil {
		slog.Error("adminui: bcrypt driver requires panel_accounts — DATABASE_URL must be set and accounts.Bootstrap must have run; admin disabled")
		return nil, false
	}
	encKey, err := totpEncryptionKey()
	if err != nil {
		slog.Error("adminui: " + err.Error() + "; admin disabled")
		return nil, false
	}
	a := auth.NewBcryptTOTPAuth(auth.BcryptConfig{
		Store:             acctStore,
		HMACKey:           []byte(hmacKey),
		BasePath:          adminBasePath,
		SessionTTL:        12 * time.Hour,
		Secure:            true,
		RateLimiter:       accounts.NewLoginLimiter(),
		LoginRate:         auth.RateRule{Limit: loginRateLimit, Window: loginRateWindow},
		TOTPRate:          auth.RateRule{Limit: totpRateLimit, Window: totpRateWindow},
		ClientIP:          proxiedClientIP,
		TOTPEncryptionKey: encKey,
	})
	return &driver{
		authn:      a,
		resolver:   sessionTenantResolver{a: a},
		authorizer: accountMatchAuthorizer{},
		totpKey:    encKey,
	}, true
}

// hmacDriver builds the single-operator rollback driver (ADR-17). The pin slug
// is the operator account UUID when one is seeded (stable identity shared with
// the bcrypt path and the future mcp_api_keys account FK), else the "operator"
// sentinel — either way exactly one tenant is ever resolvable under hmac.
func hmacDriver(hmacKey, password, adminUser, operatorID string) *driver {
	slug := operatorID
	if slug == "" {
		slug = accounts.SingleOperatorSlug
	}
	return &driver{
		authn: auth.NewHMACAuth(auth.HMACConfig{
			Username:   adminUser,
			Password:   password,
			HMACKey:    []byte(hmacKey),
			BasePath:   adminBasePath,
			SessionTTL: 12 * time.Hour,
			Secure:     true,
		}),
		resolver:   pinnedTenantResolver{slug: slug},
		authorizer: pinnedTenantAuthorizer{slug: slug},
	}
}

// sessionTenantResolver resolves Tenant{CitySlug: session.UserID} — the
// account UUID is the tenant slug (ADR-1/ADR-5). Resolve runs BEFORE
// auth.Require (resource.withTenantResolution wraps the mux pre-dispatch), so
// auth.SessionFrom(ctx) is not yet populated; SessionFromRequest performs the
// same live, revocation-checked session lookup against the request itself —
// the upstream method exists for exactly this seam. A request with no valid
// session resolves to the zero Tenant — non-global, denied by
// accountMatchAuthorizer. Fail-closed on both axes.
type sessionTenantResolver struct {
	a *auth.BcryptTOTPAuth
}

// Resolve implements tenant.Resolver.
func (r sessionTenantResolver) Resolve(req *http.Request) tenant.Tenant {
	sess, ok := r.a.SessionFromRequest(req)
	if !ok {
		return tenant.Tenant{}
	}
	return tenant.Tenant{CitySlug: sess.UserID}
}

// accountMatchAuthorizer allows only the tenant equal to the authenticated
// session's account id. It runs INSIDE auth.Require (resource.guard composes
// Require(requireTenant(h))), so auth.SessionFrom(ctx) IS populated here —
// unlike the Resolver, which runs pre-dispatch. A missing session or a
// resolver/session disagreement denies (403) — never fail-open.
type accountMatchAuthorizer struct{}

// Authorized implements tenant.Authorizer.
func (accountMatchAuthorizer) Authorized(ctx context.Context, t tenant.Tenant) (bool, error) {
	sess, ok := auth.SessionFrom(ctx)
	if !ok || sess == nil {
		return false, nil
	}
	return sess.UserID == t.CitySlug, nil
}

// pinnedTenantResolver resolves every request to the same static slug — the
// ADR-17 single-operator pin. Under HMAC no session exists, so the tenant must
// come from configuration, not the request.
type pinnedTenantResolver struct {
	slug string
}

// Resolve implements tenant.Resolver.
func (r pinnedTenantResolver) Resolve(*http.Request) tenant.Tenant {
	return tenant.Tenant{CitySlug: r.slug}
}

// pinnedTenantAuthorizer allows only the pinned slug. Paired with
// pinnedTenantResolver this makes exactly one tenant reachable; the authorizer
// is still checked per route so a future resolver change cannot silently widen
// the reachable set.
type pinnedTenantAuthorizer struct {
	slug string
}

// Authorized implements tenant.Authorizer. constant-time comparison is
// unnecessary — the slug is not a secret; equality is enough.
func (a pinnedTenantAuthorizer) Authorized(_ context.Context, t tenant.Tenant) (bool, error) {
	return t.CitySlug == a.slug, nil
}

// totpEncryptionKey reads ADMIN_TOTP_ENC_KEY — exactly
// auth.TOTPEncryptionKeyLen (32) bytes, accepted either raw (len==32) or
// hex-encoded (64 chars). Distinct env from ADMIN_HMAC_KEY on purpose: the
// session-signing key and the TOTP-at-rest key rotate on different triggers
// (see BcryptConfig.TOTPEncryptionKey's coupling rationale).
func totpEncryptionKey() ([]byte, error) {
	s := os.Getenv("ADMIN_TOTP_ENC_KEY")
	if len(s) == auth.TOTPEncryptionKeyLen {
		return []byte(s), nil
	}
	if b, err := hex.DecodeString(s); err == nil && len(b) == auth.TOTPEncryptionKeyLen {
		return b, nil
	}
	return nil, fmt.Errorf("ADMIN_TOTP_ENC_KEY must be %d bytes (raw) or %d hex chars, got %d bytes", auth.TOTPEncryptionKeyLen, auth.TOTPEncryptionKeyLen*2, len(s))
}

// proxiedClientIP extracts the client IP for login throttling
// (BcryptConfig.ClientIP). The admin listener is only reachable behind Caddy
// (compose publishes 127.0.0.1:8896), which appends the real peer to
// X-Forwarded-For — the LAST hop is the trustworthy one under a single trusted
// proxy (a client-controlled spoof would be an earlier hop). Direct local
// requests (no XFF) fall back to RemoteAddr's host.
func proxiedClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		var last string
		for start := 0; start <= len(xff); {
			end := start
			for end < len(xff) && xff[end] != ',' {
				end++
			}
			if hop := trimSpace(xff[start:end]); hop != "" {
				last = hop
			}
			start = end + 1
		}
		if last != "" {
			return last
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}
