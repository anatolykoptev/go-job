package accounts_test

// End-to-end ctx-extraction tests for accounts.AccountFrom: the pure deny
// matrix lives in identity_internal_test.go; here the REAL middlewares stamp
// the ctx — RequireBearerToken for the MCP leg, BcryptTOTPAuth.Require (real
// login on ephemeral pg) for the web leg.

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
	"github.com/anatolykoptev/go_job/internal/accounts"
	"github.com/google/uuid"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/stretchr/testify/require"
)

// fixedVerifier maps canned tokens to TokenInfo — the shape KeyStore.verify
// produces, without needing a database for the middleware-level assertions.
func fixedVerifier(t *testing.T, want uuid.UUID) sdkauth.TokenVerifier {
	t.Helper()
	return func(_ context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
		switch token {
		case "good-token":
			return &sdkauth.TokenInfo{UserID: want.String(), Expiration: time.Now().Add(time.Hour)}, nil
		case "badid-token":
			return &sdkauth.TokenInfo{UserID: "not-a-uuid", Expiration: time.Now().Add(time.Hour)}, nil
		default:
			return nil, sdkauth.ErrInvalidToken
		}
	}
}

// TestAccountFrom_TokenInfoEndToEnd proves the MCP leg through the real SDK
// middleware: RequireBearerToken stamps TokenInfo on the request ctx and
// AccountFrom resolves the account UUID inside the handler. Wrong token →
// 401 before the handler ever runs; a verifier that mints a malformed UserID
// authenticates the request but fails AccountFrom closed.
func TestAccountFrom_TokenInfoEndToEnd(t *testing.T) {
	want := uuid.New()
	var (
		got    uuid.UUID
		gotOK  bool
		called bool
	)
	handler := sdkauth.RequireBearerToken(fixedVerifier(t, want), nil)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			got, gotOK = accounts.AccountFrom(r.Context())
			w.WriteHeader(http.StatusNoContent)
		}),
	)

	// No Authorization header → 401, handler never reached.
	rw := httptest.NewRecorder()
	handler.ServeHTTP(rw, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	require.Equal(t, http.StatusUnauthorized, rw.Code)
	require.False(t, called)

	// Verified token carrying the account UUID → AccountFrom resolves.
	got, gotOK, called = uuid.Nil, false, false
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer good-token")
	rw = httptest.NewRecorder()
	handler.ServeHTTP(rw, req)
	require.Equal(t, http.StatusNoContent, rw.Code)
	require.True(t, called && gotOK)
	require.Equal(t, want, got)

	// Verified token with malformed UserID → handler runs, AccountFrom denies.
	got, gotOK = uuid.Nil, true
	req = httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer badid-token")
	rw = httptest.NewRecorder()
	handler.ServeHTTP(rw, req)
	require.True(t, called)
	require.False(t, gotOK, "malformed TokenInfo.UserID must deny — a verifier bug must not mint an identity")
}

// TestMCPTenantResolver_EndToEnd pins the ADR-16 contract used by panelmcp:
// inside a bearer-authed call ctx the resolver returns Tenant{CitySlug:
// account UUID}; on a bare ctx (no verified identity — e.g. bearerAuth nil)
// it denies, which upstream callTenant turns into a rejected tool call.
func TestMCPTenantResolver_EndToEnd(t *testing.T) {
	want := uuid.New()

	// Bare ctx — nothing stamped → deny (the no-DB/no-auth shape stays closed).
	tn, ok := accounts.MCPTenantResolver(context.Background())
	require.False(t, ok)
	require.Equal(t, "", tn.CitySlug)

	handler := sdkauth.RequireBearerToken(fixedVerifier(t, want), nil)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tn, ok = accounts.MCPTenantResolver(r.Context())
			w.WriteHeader(http.StatusNoContent)
		}),
	)
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer good-token")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	require.True(t, ok)
	require.Equal(t, want.String(), tn.CitySlug,
		"tenant must pin to the bearer key's account UUID")
}

// TestAccountFrom_SessionEndToEnd proves the web leg on a real database:
// password login → session cookie → Require stamps auth.Session →
// AccountFrom resolves the same account UUID the MCP leg would produce.
// Mutating the session extraction (or the uuid gate) fails this test.
func TestAccountFrom_SessionEndToEnd(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	dropAccountTables(t, pool)
	acctStore, op, err := accounts.Bootstrap(ctx, pool,
		accounts.OperatorSeed{Email: "edge@t.example", Password: "edge-pass-123"})
	require.NoError(t, err)
	require.NotNil(t, op)

	a := auth.NewBcryptTOTPAuth(auth.BcryptConfig{
		Store:             acctStore,
		HMACKey:           []byte("0123456789abcdef0123456789abcdef"),
		BasePath:          "/admin",
		SessionTTL:        time.Hour,
		Secure:            true,
		RateLimiter:       accounts.NewLoginLimiter(),
		LoginRate:         auth.RateRule{Limit: 5, Window: time.Minute},
		TOTPRate:          auth.RateRule{Limit: 10, Window: time.Minute},
		TOTPEncryptionKey: []byte(strings.Repeat("k", 32)),
	})

	// Real login → session cookie.
	form := url.Values{"email": {"edge@t.example"}, "password": {"edge-pass-123"}}
	lw := httptest.NewRecorder()
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/login",
		strings.NewReader(form.Encode()))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	a.LoginHandler().ServeHTTP(lw, loginReq)
	require.Equal(t, http.StatusSeeOther, lw.Code, "password-only login must issue a session")
	var sessCookie *http.Cookie
	for _, c := range lw.Result().Cookies() {
		if c.Name == a.SessionCookieName() {
			sessCookie = c
		}
	}
	require.NotNil(t, sessCookie)

	// Require stamps the session; inside, AccountFrom resolves the UUID.
	var (
		gotID uuid.UUID
		gotOK bool
	)
	authed := a.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID, gotOK = accounts.AccountFrom(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/admin/x", nil)
	req.AddCookie(sessCookie)
	authed.ServeHTTP(httptest.NewRecorder(), req)
	require.True(t, gotOK, "web session must resolve through AccountFrom")
	require.Equal(t, op.ID, gotID.String())
}

// TestLoopbackBypass_NeverEnabled_SourceGate regression-guards the RemoteAddr
// trap (ADR-3/ADR-4): go-job serves behind Caddy, where every request arrives
// with RemoteAddr=loopback — a `LoopbackBypass: true` in main.go disables
// bearer auth on ALL external traffic. Assert it can never be reintroduced,
// and that every BearerAuth literal pins it explicitly false.
func TestLoopbackBypass_NeverEnabled_SourceGate(t *testing.T) {
	src, err := os.ReadFile("../../main.go")
	require.NoError(t, err)
	s := string(src)

	require.NotContains(t, s, "LoopbackBypass: true",
		"behind Caddy loopback bypass = auth disabled — keep it false on every BearerAuth")
	require.NotContains(t, s, "LoopbackBypass:true",
		"behind Caddy loopback bypass = auth disabled — keep it false on every BearerAuth")
	require.GreaterOrEqual(t, strings.Count(s, "LoopbackBypass: false"), 2,
		"both BearerAuth literals (edge :8891 + panelmcp :8897) must pin LoopbackBypass false explicitly")
}

// TestMCPEdgeWiring_SourceGate guards the load-bearing wiring ordering:
// the legacy-token seed runs inside initEngine AFTER the hunt migration
// runner (migration 014 owns mcp_api_keys); the DB verifier is mounted on
// BOTH listeners; panelmcp always carries the fail-closed TenantResolver.
func TestMCPEdgeWiring_SourceGate(t *testing.T) {
	src, err := os.ReadFile("../../main.go")
	require.NoError(t, err)
	s := string(src)

	body := func(name string) string {
		i := strings.Index(s, "func "+name+"(")
		require.Positive(t, i, name+" definition missing from main.go")
		rest := s[i:]
		if j := strings.Index(rest[1:], "\nfunc "); j >= 0 {
			return rest[:j+1]
		}
		return rest
	}

	init := body("initEngine")
	migrate := strings.Index(init, "hStore.Migrate(")
	seed := strings.Index(init, "seedLegacyEdgeToken(")
	require.Positive(t, migrate, "hStore.Migrate call missing from initEngine")
	require.Positive(t, seed, "seedLegacyEdgeToken call missing from initEngine (ADR-4 seed must run on the DB-ready path)")
	require.Less(t, migrate, seed,
		"ADR-4: the legacy-token seed must run AFTER hStore.Migrate — migration 014 creates mcp_api_keys")

	require.Equal(t, 2, strings.Count(s, "keyStore.Verifier()"),
		"the DB verifier must be mounted on BOTH listeners — :8891 edge and :8897 panelmcp")

	admin := body("startAdminServer")
	require.Contains(t, admin, "accounts.MCPTenantResolver",
		"panelmcp must wire the fail-closed TenantResolver unconditionally")
	require.Contains(t, admin, "BearerAuth:",
		"panelmcp must wire BearerAuth — an unauthenticated admin MCP exposes all Resources")
}
