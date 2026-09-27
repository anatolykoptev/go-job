package adminui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/go-panel/auth"
	"github.com/anatolykoptev/go-panel/tenant"
	"github.com/anatolykoptev/go_job/internal/accounts"
	"github.com/anatolykoptev/go_job/internal/dbtest"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

const testHMACKey = "0123456789abcdef0123456789abcdef" // 32 bytes

// TestSelectDriver_HMAC covers the ADR-17 rollback lever: AUTH_DRIVER=hmac
// returns HMACAuth and pins every request to the operator slug — the static
// single-operator mode. No DB needed: the pin works without a seeded account
// (falls back to the sentinel).
func TestSelectDriver_HMAC(t *testing.T) {
	t.Setenv("AUTH_DRIVER", "hmac")

	d, ok := selectDriver(nil, "", testHMACKey, "pw", "admin")
	require.True(t, ok)
	require.IsType(t, &auth.HMACAuth{}, d.authn)
	require.Nil(t, d.totpKey, "hmac mode must not wire TOTP enrollment")

	// No operator account → sentinel slug pinned.
	req := httptest.NewRequest(http.MethodGet, "/admin/jobs", nil)
	got := d.resolver.Resolve(req)
	require.Equal(t, accounts.SingleOperatorSlug, got.CitySlug)

	okA, err := d.authorizer.Authorized(context.Background(), tenant.Tenant{CitySlug: accounts.SingleOperatorSlug})
	require.NoError(t, err)
	require.True(t, okA)
	okD, err := d.authorizer.Authorized(context.Background(), tenant.Tenant{CitySlug: "foreign"})
	require.NoError(t, err)
	require.False(t, okD, "a non-pinned tenant is denied even in hmac mode")

	// Seeded operator → its UUID is the pin.
	d2, ok := selectDriver(nil, "uuid-1234", testHMACKey, "pw", "admin")
	require.True(t, ok)
	require.Equal(t, "uuid-1234", d2.resolver.Resolve(req).CitySlug)
}

// TestSelectDriver_BcryptRequiresStoreAndKey proves the default driver fails
// closed: no account store or no ADMIN_TOTP_ENC_KEY → admin disabled, never
// half-configured.
func TestSelectDriver_BcryptRequiresStoreAndKey(t *testing.T) {
	t.Setenv("AUTH_DRIVER", "")
	t.Setenv("ADMIN_TOTP_ENC_KEY", strings.Repeat("ab", 32))

	_, ok := selectDriver(nil, "", testHMACKey, "pw", "admin")
	require.False(t, ok, "nil account store must disable the bcrypt driver")

	t.Setenv("ADMIN_TOTP_ENC_KEY", "short")
	_, ok = selectDriver(auth.NewPgxAccountStore(nil), "", testHMACKey, "pw", "admin")
	require.False(t, ok, "invalid TOTP key must disable the bcrypt driver")
}

// TestSelectDriver_BcryptWiresSessionSeam asserts the bcrypt driver ships the
// ADR-5 tenant seam: a request without a session resolves to the zero tenant
// (non-global → deny), and the authorizer denies without a stamped session.
func TestSelectDriver_BcryptWiresSessionSeam(t *testing.T) {
	t.Setenv("AUTH_DRIVER", "")
	t.Setenv("ADMIN_TOTP_ENC_KEY", strings.Repeat("ab", 32))

	d, ok := selectDriver(auth.NewPgxAccountStore(nil), "", testHMACKey, "pw", "admin")
	require.True(t, ok)
	require.IsType(t, &auth.BcryptTOTPAuth{}, d.authn)
	require.NotNil(t, d.totpKey)

	req := httptest.NewRequest(http.MethodGet, "/admin/jobs", nil)
	got := d.resolver.Resolve(req)
	require.Equal(t, "", got.CitySlug, "no session → zero tenant (non-global → denied)")

	// No Session in ctx → deny; session that disagrees with the tenant → deny.
	okA, err := d.authorizer.Authorized(context.Background(), tenant.Tenant{CitySlug: "anyone"})
	require.NoError(t, err)
	require.False(t, okA)
}

// TestSessionTenantSeam_EndToEnd is the DB-gated proof of the ADR-5 seam:
// real login → session cookie → SessionFromRequest resolves Tenant{UserID} →
// Require stamps the session → accountMatchAuthorizer allows the matching
// tenant and denies a foreign one. Mutating any link (resolver returning a
// static/global tenant, authorizer always-allow) fails this test.
func TestSessionTenantSeam_EndToEnd(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	dbtest.RequireTestDB(t, dsn)
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	ctx := context.Background()

	_, err = pool.Exec(ctx,
		`DROP TABLE IF EXISTS panel_totp_recovery_codes; DROP TABLE IF EXISTS panel_accounts`)
	require.NoError(t, err)
	acctStore, op, err := accounts.Bootstrap(ctx, pool,
		accounts.OperatorSeed{Email: "seam@t.example", Password: "seam-pass-123"})
	require.NoError(t, err)
	require.NotNil(t, op)

	a := auth.NewBcryptTOTPAuth(auth.BcryptConfig{
		Store:             acctStore,
		HMACKey:           []byte(testHMACKey),
		BasePath:          adminBasePath,
		SessionTTL:        time.Hour,
		Secure:            true,
		RateLimiter:       accounts.NewLoginLimiter(),
		LoginRate:         auth.RateRule{Limit: loginRateLimit, Window: loginRateWindow},
		TOTPRate:          auth.RateRule{Limit: totpRateLimit, Window: totpRateWindow},
		TOTPEncryptionKey: []byte(strings.Repeat("k", 32)),
	})
	resolver := sessionTenantResolver{a: a}
	authz := accountMatchAuthorizer{}

	// Real login: POST credentials → session cookie.
	form := url.Values{"email": {"seam@t.example"}, "password": {"seam-pass-123"}}
	loginReq := httptest.NewRequest(http.MethodPost, adminBasePath+"/login",
		strings.NewReader(form.Encode()))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	lw := httptest.NewRecorder()
	a.LoginHandler().ServeHTTP(lw, loginReq)
	require.Equal(t, http.StatusSeeOther, lw.Code, "password-only login must issue a session")
	var sessCookie *http.Cookie
	for _, c := range lw.Result().Cookies() {
		if c.Name == a.SessionCookieName() {
			sessCookie = c
		}
	}
	require.NotNil(t, sessCookie)

	// Resolver: cookie → Tenant{account UUID}.
	req := httptest.NewRequest(http.MethodGet, "/admin/jobs", nil)
	req.AddCookie(sessCookie)
	got := resolver.Resolve(req)
	require.Equal(t, op.ID, got.CitySlug, "tenant must resolve to the session account UUID")

	// Authorizer inside Require-stamped ctx: match allows, foreign denies.
	var allowedTenant, deniedTenant bool
	capture := func(tt tenant.Tenant, dst *bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			ok, err := authz.Authorized(r.Context(), tt)
			require.NoError(t, err)
			*dst = ok
			w.WriteHeader(http.StatusNoContent)
		}
	}
	a.Require(capture(tenant.Tenant{CitySlug: op.ID}, &allowedTenant)).ServeHTTP(httptest.NewRecorder(), req)
	require.True(t, allowedTenant, "tenant == session account must be authorized")

	a.Require(capture(tenant.Tenant{CitySlug: "00000000-0000-0000-0000-000000000000"}, &deniedTenant)).ServeHTTP(httptest.NewRecorder(), req)
	require.False(t, deniedTenant, "foreign tenant must be denied inside a valid session")
}

// TestTOTPEncryptionKey covers the env contract: raw 32-byte and 64-hex forms
// accepted, everything else rejected (the driver then disables admin —
// fail-closed misconfiguration, never a panic at request time).
func TestTOTPEncryptionKey(t *testing.T) {
	t.Setenv("ADMIN_TOTP_ENC_KEY", strings.Repeat("k", 32))
	b, err := totpEncryptionKey()
	require.NoError(t, err)
	require.Len(t, b, auth.TOTPEncryptionKeyLen)

	t.Setenv("ADMIN_TOTP_ENC_KEY", strings.Repeat("ab", 32))
	b, err = totpEncryptionKey()
	require.NoError(t, err)
	require.Len(t, b, auth.TOTPEncryptionKeyLen)

	for _, bad := range []string{"", "short", strings.Repeat("zz", 32), strings.Repeat("ab", 33)} {
		t.Setenv("ADMIN_TOTP_ENC_KEY", bad)
		_, err = totpEncryptionKey()
		require.Error(t, err, "key %q must be rejected", bad)
	}
}

// TestProxiedClientIP: single trusted proxy (Caddy) → the LAST X-Forwarded-For
// hop is the peer Caddy saw; earlier hops are client-controllable. No XFF →
// RemoteAddr host.
func TestProxiedClientIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	req.RemoteAddr = "10.0.0.9:4433"
	require.Equal(t, "10.0.0.9", proxiedClientIP(req))

	req.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.7")
	require.Equal(t, "203.0.113.7", proxiedClientIP(req), "last XFF hop wins")

	req.Header.Set("X-Forwarded-For", "")
	require.Equal(t, "10.0.0.9", proxiedClientIP(req))
}

// TestLoginLimiter proves the fixed-window contract auth relies on: limit
// enforcement, window reset, and fail-closed on ctx error.
func TestLoginLimiter(t *testing.T) {
	l := accounts.NewLoginLimiter()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		ok, err := l.Allow(ctx, "ip1", 3, time.Minute)
		require.NoError(t, err)
		require.True(t, ok, "hit %d under limit must allow", i)
	}
	ok, err := l.Allow(ctx, "ip1", 3, time.Minute)
	require.NoError(t, err)
	require.False(t, ok, "over-limit must deny")

	// Different key has its own window.
	ok, err = l.Allow(ctx, "ip2", 3, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	// Cancelled ctx → error → caller denies (fail-closed contract).
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = l.Allow(cancelled, "ip3", 3, time.Minute)
	require.Error(t, err)
}
