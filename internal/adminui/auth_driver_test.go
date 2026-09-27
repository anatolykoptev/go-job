package adminui

import (
	"bytes"
	"context"
	"log/slog"
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
	"github.com/anatolykoptev/go_job/internal/engine/jobs/applications"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/google/uuid"
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

	dbtest.DropAccountTables(t, pool)
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

// TestProxiedClientIP: XFF is honoured ONLY for a trusted immediate peer —
// loopback (host-local proxy) or private/ULA (docker bridge gateway /
// in-network Caddy container, which is what the compose-published
// 127.0.0.1:8896 mapping actually presents). A direct routable peer's XFF is
// client-controllable spoof material — ignored, RemoteAddr wins.
func TestProxiedClientIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	req.RemoteAddr = "10.0.0.9:4433" // private peer, no XFF
	require.Equal(t, "10.0.0.9", proxiedClientIP(req))

	// Trusted peers → last XFF hop wins (earlier hops are client-controllable).
	for _, peer := range []string{"127.0.0.1:4433", "10.0.0.9:4433", "172.18.0.1:4433", "192.168.1.5:4433"} {
		req.RemoteAddr = peer
		req.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.7")
		require.Equal(t, "203.0.113.7", proxiedClientIP(req), "loopback/private peer %s → last XFF hop", peer)
	}

	// Untrusted (routable) peer → XFF ignored entirely.
	req.RemoteAddr = "203.0.113.99:4433"
	require.Equal(t, "203.0.113.99", proxiedClientIP(req), "routable peer's XFF is attacker-controlled — must be ignored")

	// Trusted peer with empty/malformed XFF → RemoteAddr.
	req.RemoteAddr = "127.0.0.1:4433"
	req.Header.Set("X-Forwarded-For", "")
	require.Equal(t, "127.0.0.1", proxiedClientIP(req))
	req.Header.Set("X-Forwarded-For", "  , ,")
	require.Equal(t, "127.0.0.1", proxiedClientIP(req), "all-empty XFF hops fall back to RemoteAddr")
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

// TestLoginLimiter_ZeroValue: a bare LoginLimiter{} must satisfy
// auth.RateLimiter without NewLoginLimiter — Allow lazily initialises
// hits/now, so the zero value never nil-derefs.
func TestLoginLimiter_ZeroValue(t *testing.T) {
	var l accounts.LoginLimiter
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		ok, err := l.Allow(ctx, "ip", 2, time.Minute)
		require.NoError(t, err)
		require.True(t, ok, "hit %d under limit must allow on a zero-value limiter", i)
	}
	ok, err := l.Allow(ctx, "ip", 2, time.Minute)
	require.NoError(t, err)
	require.False(t, ok, "over-limit must deny on a zero-value limiter")
}

// captureSlog swaps the default logger for a buffer-backed text handler —
// the MED-3 assertions below read the emitted records, not just the return
// values (the findings are "log loud, don't disable" so the LOG is the
// contract under test).
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

// TestSelectDriver_BcryptZeroLoginable_Logs covers the zero-loginable trap:
// bcrypt driver + ADMIN_PASSWORD set but no seed identifier → admin enabled
// with nobody able to log in — must scream ADMIN_EMAIL/ADMIN_USERNAME, not
// disable (CLI-provisioned accounts may still work) and not stay silent.
func TestSelectDriver_BcryptZeroLoginable_Logs(t *testing.T) {
	t.Setenv("AUTH_DRIVER", "")
	t.Setenv("ADMIN_TOTP_ENC_KEY", strings.Repeat("ab", 32))
	t.Setenv("ADMIN_EMAIL", "")
	t.Setenv("ADMIN_USERNAME", "")

	buf := captureSlog(t)
	d, ok := selectDriver(auth.NewPgxAccountStore(nil), "", testHMACKey, "pw", "admin")
	require.True(t, ok, "admin stays enabled — accounts provisioned out-of-band may still log in")
	require.NotNil(t, d)
	require.Contains(t, buf.String(), "no operator account was seeded")
	require.Contains(t, buf.String(), "ADMIN_EMAIL")
}

// TestSelectDriver_BcryptNonEmailSeed_Logs: an ADMIN_USERNAME-only seed
// (e.g. "admin") produces a non-email identifier the login form's
// <input type=email> cannot submit — the driver must name ADMIN_EMAIL.
func TestSelectDriver_BcryptNonEmailSeed_Logs(t *testing.T) {
	t.Setenv("AUTH_DRIVER", "")
	t.Setenv("ADMIN_TOTP_ENC_KEY", strings.Repeat("ab", 32))
	t.Setenv("ADMIN_EMAIL", "")
	t.Setenv("ADMIN_USERNAME", "admin") // non-email-shaped identifier

	buf := captureSlog(t)
	d, ok := selectDriver(auth.NewPgxAccountStore(nil), "uuid-1", testHMACKey, "pw", "admin")
	require.True(t, ok)
	require.NotNil(t, d)
	require.Contains(t, buf.String(), "not email-shaped")
	require.Contains(t, buf.String(), "ADMIN_EMAIL")
}

// TestNew_WiresSessionTenantGate is the §5b amputation gate for
// adminui.New's resource.Config: it drives a REAL bcrypt login through the
// assembled panel and then GETs a guarded route
// (/admin/security/totp/enroll — chosen because it touches only
// panel_accounts, no hunt tables). Expected: 200 — sessionTenantResolver
// resolved the session account UUID and accountMatchAuthorizer allowed it.
//
// Mutation contract (both must turn this test RED):
//   - delete `Resolver: d.resolver` → Config falls back to
//     tenant.PathResolver{Segment:2} → resolves the GLOBAL tenant ("spb")
//     for this path → accountMatchAuthorizer denies (uuid ≠ spb) → 403;
//   - delete `TenantAuthorizer: d.authorizer` → GlobalOnlyAuthorizer denies
//     the non-global account tenant → 403.
func TestNew_WiresSessionTenantGate(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	dbtest.RequireTestDB(t, dsn)
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	ctx := context.Background()

	dbtest.DropAccountTables(t, pool)
	acctStore, op, err := accounts.Bootstrap(ctx, pool,
		accounts.OperatorSeed{Email: "gate@t.example", Password: "gate-pass-123"})
	require.NoError(t, err)
	require.NotNil(t, op)

	t.Setenv("AUTH_DRIVER", "")
	t.Setenv("ADMIN_HMAC_KEY", testHMACKey)
	t.Setenv("ADMIN_PASSWORD", "unused-hmac-pw")
	t.Setenv("ADMIN_TOTP_ENC_KEY", strings.Repeat("ab", 32))
	t.Setenv("ADMIN_CSRF_KEY", strings.Repeat("cd", 32))
	t.Setenv("ADMIN_EMAIL", "gate@t.example")
	t.Setenv("ADMIN_USERNAME", "")

	handler, _, ok := New(hunt.NewStore(pool), applications.New(nil, t.TempDir(), uuid.MustParse(op.ID)), acctStore, op.ID)
	require.True(t, ok, "bcrypt driver must be enabled with a bootstrapped store")

	// Real login through the assembled handler → session cookie.
	form := url.Values{"email": {"gate@t.example"}, "password": {"gate-pass-123"}}
	loginReq := httptest.NewRequest(http.MethodPost, adminBasePath+"/login", strings.NewReader(form.Encode()))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	lw := httptest.NewRecorder()
	handler.ServeHTTP(lw, loginReq)
	require.Equal(t, http.StatusSeeOther, lw.Code, "password-only login must issue a session")
	cookies := lw.Result().Cookies()
	require.NotEmpty(t, cookies, "login must set a session cookie")

	// The guarded probe: resolver+authorizer must resolve-and-allow the
	// session's own tenant. Either Config amputation collapses to a deny.
	req := httptest.NewRequest(http.MethodGet, adminBasePath+"/security/totp/enroll/", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code,
		"guarded route must be reachable for the session's own tenant — a 403 here means the Resolver/TenantAuthorizer fields were dropped from resource.New's Config (§5b)")
	require.Contains(t, w.Body.String(), `name="current_password"`,
		"the enroll gate renders the re-auth form (MED-4 step-up present)")
}

// TestTenantGateFields_SourceGate closes the mutation hole TestNew_WiresSessionTenantGate
// cannot: deleting EITHER Config field alone flips the round-trip test RED, but
// deleting BOTH Resolver and TenantAuthorizer collapses resource.New to the
// pre-multi-account defaults (PathResolver -> global "spb" tenant +
// GlobalOnlyAuthorizer allow) and the suite stays green — the exact silent
// regression this gate exists to catch. Assert the Config literal carries both
// fields under both AUTH_DRIVER modes.
func TestTenantGateFields_SourceGate(t *testing.T) {
	src, err := os.ReadFile("adminui.go")
	require.NoError(t, err)
	s := string(src)

	i := strings.Index(s, "resource.New(resource.Config{")
	require.Positive(t, i, "resource.New Config literal missing from adminui.go")
	cfg := s[i:]
	j := strings.Index(cfg, "})")
	require.Positive(t, j, "resource.New Config literal unterminated")
	cfg = cfg[:j]
	require.Contains(t, cfg, "Resolver:",
		"resource.Config must wire Resolver — deleting it falls back to the global-tenant PathResolver")
	require.Contains(t, cfg, "TenantAuthorizer:",
		"resource.Config must wire TenantAuthorizer — deleting it falls back to GlobalOnlyAuthorizer")
}
