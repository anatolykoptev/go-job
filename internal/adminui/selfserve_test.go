package adminui

// selfserve_test.go — the P6 deny/fail-closed matrix legs for the self-serve
// surfaces (spec §Deny matrix): public register, /admin/keys, /admin/accounts.
// Every leg drives the REAL assembled handler (New + bcrypt login) or the
// real registerHandler — the point is that each protection is exercised at
// the seam the request actually crosses, so removing the protection turns
// exactly one leg RED.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/go-panel/auth"
	"github.com/anatolykoptev/go-panel/csrf"
	"github.com/anatolykoptev/go_job/internal/accounts"
	"github.com/anatolykoptev/go_job/internal/dbtest"
	"github.com/anatolykoptev/go_job/internal/engine/jobs/applications"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// selfServeFixture bundles a fully-wired New() handler (bcrypt driver) plus
// the accounts substrate the legs exercise.
type selfServeFixture struct {
	handler   http.Handler
	pool      *pgxpool.Pool
	acctStore *auth.PgxAccountStore
	keyStore  *accounts.KeyStore
	op        *auth.Account // seeded admin
	csrfKey   string
}

func newSelfServeFixture(t *testing.T) *selfServeFixture {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	dbtest.RequireTestDB(t, dsn)
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	ctx := context.Background()

	dbtest.DropAccountTables(t, pool)
	acctStore, op, err := accounts.Bootstrap(ctx, pool,
		accounts.OperatorSeed{Email: "admin-selfserve@t.example", Password: "admin-pass-12345"})
	require.NoError(t, err)
	require.NotNil(t, op)

	t.Setenv("AUTH_DRIVER", "")
	t.Setenv("ADMIN_HMAC_KEY", testHMACKey)
	t.Setenv("ADMIN_PASSWORD", "unused-hmac-pw")
	t.Setenv("ADMIN_TOTP_ENC_KEY", strings.Repeat("ab", 32))
	csrfKey := strings.Repeat("cd", 32)
	t.Setenv("ADMIN_CSRF_KEY", csrfKey)
	t.Setenv("ADMIN_EMAIL", "admin-selfserve@t.example")
	t.Setenv("ADMIN_USERNAME", "")

	keyStore := accounts.NewKeyStore(pool)
	handler, _, ok := New(hunt.NewStore(pool), applications.New(nil, t.TempDir(), uuid.MustParse(op.ID)), acctStore, keyStore, op.ID)
	require.True(t, ok, "bcrypt driver must be enabled with a bootstrapped store")
	return &selfServeFixture{handler, pool, acctStore, keyStore, op, csrfKey}
}

// selfServeLogin drives a REAL bcrypt login and returns the session cookies.
func selfServeLogin(t *testing.T, h http.Handler, email, pw string) []*http.Cookie {
	t.Helper()
	form := url.Values{"email": {email}, "password": {pw}}
	req := httptest.NewRequest(http.MethodPost, adminBasePath+"/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	require.Equal(t, http.StatusSeeOther, w.Code, "login %s must issue a session: %s", email, w.Body.String())
	cookies := w.Result().Cookies()
	require.NotEmpty(t, cookies, "login must set a session cookie")
	return cookies
}

// sessionToken issues a CSRF token bound to the session cookie — the same
// binding MountAction's csrfProtect verifies (sessionValue = cookie value).
func sessionToken(csrfKey string, cookies []*http.Cookie) string {
	for _, c := range cookies {
		if c.Name == "panel_admin" {
			return csrf.Issue([]byte(csrfKey), c.Value, csrf.DefaultTTL)
		}
	}
	return ""
}

// selfServePost issues a POST to an admin route with session cookies + a
// session-bound CSRF token.
func selfServePost(t *testing.T, h http.Handler, cookies []*http.Cookie, csrfKey, path string, fields url.Values) *httptest.ResponseRecorder {
	t.Helper()
	if fields == nil {
		fields = url.Values{}
	}
	fields.Set(csrf.FormField, sessionToken(csrfKey, cookies))
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(fields.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// selfServeGet issues a GET to an admin route with session cookies.
func selfServeGet(h http.Handler, cookies []*http.Cookie, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// newUserAccount seeds an active, passworded role='user' account — the
// self-serve caller shape.
func newUserAccount(t *testing.T, pool *pgxpool.Pool, email, pw string) uuid.UUID {
	t.Helper()
	hash, err := auth.HashPassword(pw)
	require.NoError(t, err)
	id, created, err := accounts.CreateAccount(context.Background(), pool, email, "test user", &hash, "user")
	require.NoError(t, err)
	require.True(t, created)
	return id
}

// ─── register legs ────────────────────────────────────────────────────────

var regCSRFRe = regexp.MustCompile(`name="_csrf" value="([^"]+)"`)

// registerGET fetches the form: returns the fresh nonce cookie + the bound
// CSRF token (each GET mints a NEW nonce — pairing is per-request).
func registerGET(t *testing.T, h http.Handler) (*http.Cookie, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, adminBasePath+"/register", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "GET register must render 200")
	var nonce *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == regNonceCookie {
			nonce = c
		}
	}
	require.NotNil(t, nonce, "GET must set the anonymous nonce cookie")
	// Cookie flags are the silent-failure surface of the anonymous-CSRF
	// scheme: a dropped Secure/SameSite/Path narrows nothing visibly yet
	// widens the nonce's reachability. Pin all of them.
	require.True(t, nonce.HttpOnly, "nonce cookie must be HttpOnly")
	require.True(t, nonce.Secure, "nonce cookie must be Secure")
	require.Equal(t, http.SameSiteStrictMode, nonce.SameSite, "nonce cookie must be SameSite=Strict")
	require.Equal(t, adminBasePath+"/register", nonce.Path, "nonce cookie must be scoped to /admin/register")
	require.Equal(t, 3600, nonce.MaxAge, "nonce cookie Max-Age must stay 1h (shorter than csrf.DefaultTTL — fails closed)")
	require.Len(t, nonce.Value, 64, "nonce is 32 random bytes hex-encoded")
	m := regCSRFRe.FindStringSubmatch(w.Body.String())
	require.NotNil(t, m, "form must embed a _csrf token")
	return nonce, m[1]
}

func registerPOST(h http.Handler, nonce *http.Cookie, token string, fields url.Values) *httptest.ResponseRecorder {
	if token != "" {
		fields.Set(csrf.FormField, token)
	}
	req := httptest.NewRequest(http.MethodPost, adminBasePath+"/register", strings.NewReader(fields.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if nonce != nil {
		req.AddCookie(nonce)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func countAccounts(t *testing.T, pool *pgxpool.Pool, email string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM panel_accounts WHERE email = $1`, email).Scan(&n))
	return n
}

// TestRegister_PendingShape — spec leg register_pending_shape: a valid POST
// lands a row with active=false, role='user' (the literals are the spec).
func TestRegister_PendingShape(t *testing.T) {
	f := newSelfServeFixture(t)
	nonce, tok := registerGET(t, f.handler)

	w := registerPOST(f.handler, nonce, tok, url.Values{
		"email": {"Newuser@T.example"}, "name": {"New User"}, "password": {"long-enough-pw"}})
	require.Equal(t, http.StatusOK, w.Code, "register POST must answer the success page: %s", w.Body.String())
	require.Contains(t, w.Body.String(), "Registration received")

	var role string
	var active bool
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`SELECT role, active FROM panel_accounts WHERE email = $1`, "newuser@t.example").Scan(&role, &active),
		"the pending row must exist (email normalized to lowercase)")
	assert.Equal(t, "user", role)
	assert.False(t, active)
}

// TestRegister_NoEnumeration — spec leg register_no_enumeration: new-email
// and existing-email POSTs get byte-identical status+body, and the existing
// row is untouched.
func TestRegister_NoEnumeration(t *testing.T) {
	f := newSelfServeFixture(t)

	nonce, tok := registerGET(t, f.handler)
	wNew := registerPOST(f.handler, nonce, tok, url.Values{
		"email": {"fresh-reg@t.example"}, "name": {"N"}, "password": {"long-enough-pw"}})

	nonce2, tok2 := registerGET(t, f.handler)
	wExisting := registerPOST(f.handler, nonce2, tok2, url.Values{
		"email": {"admin-selfserve@t.example"}, "name": {"X"}, "password": {"long-enough-pw"}})

	require.Equal(t, wNew.Code, wExisting.Code, "status must not differ on existing email")
	require.Equal(t, wNew.Body.String(), wExisting.Body.String(),
		"body must be byte-identical — no enumeration oracle")

	// The seeded operator row is untouched (still admin, still active).
	var role string
	var active bool
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`SELECT role, active FROM panel_accounts WHERE email = 'admin-selfserve@t.example'`).Scan(&role, &active))
	assert.Equal(t, "admin", role)
	assert.True(t, active)
}

// TestRegister_Honeypot — spec leg register_honeypot: a filled website field
// yields the identical success page and zero new rows — even with NO nonce
// cookie or CSRF token (the honeypot check runs first, per the amendment).
func TestRegister_Honeypot(t *testing.T) {
	f := newSelfServeFixture(t)
	w := registerPOST(f.handler, nil, "", url.Values{
		"email": {"bot@t.example"}, "password": {"long-enough-pw"}, "website": {"spam.example"}})
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "Registration received",
		"honeypot must get the same success page")
	assert.Equal(t, 0, countAccounts(t, f.pool, "bot@t.example"), "honeypot must insert nothing")
}

// TestRegister_CSRF — spec leg register_csrf: no nonce cookie / no _csrf →
// generic 403, zero rows. website stays EMPTY (a filled honeypot
// legitimately returns success before the CSRF check — amendment).
func TestRegister_CSRF(t *testing.T) {
	f := newSelfServeFixture(t)

	// No cookie, no token.
	w := registerPOST(f.handler, nil, "", url.Values{
		"email": {"nocookie@t.example"}, "password": {"long-enough-pw"}})
	require.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, 0, countAccounts(t, f.pool, "nocookie@t.example"))

	// Cookie present but token missing/wrong → still 403.
	nonce, _ := registerGET(t, f.handler)
	w = registerPOST(f.handler, nonce, "forged-token", url.Values{
		"email": {"badtok@t.example"}, "password": {"long-enough-pw"}})
	require.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, 0, countAccounts(t, f.pool, "badtok@t.example"))
}

// TestRegister_RateLimit — spec leg register_ratelimit: the 6th POST inside
// the burst window gets 429 + Retry-After, zero rows. Driven against the
// real handler struct so the test can exhaust the window directly.
func TestRegister_RateLimit(t *testing.T) {
	pool := openJobsPool(t)
	dbtest.RequireTestDB(t, os.Getenv("DATABASE_URL"))
	ctx := context.Background()
	dbtest.DropAccountTables(t, pool)
	_, _, err := accounts.Bootstrap(ctx, pool, accounts.OperatorSeed{})
	require.NoError(t, err)

	h := newRegisterHandler(pool, []byte("test-csrf-key-32bytes-minimum-here!"))
	// httptest.NewRequest's RemoteAddr is 192.0.2.1 — a public (TEST-NET) IP,
	// so proxiedClientIP keys on it directly (XFF ignored).
	const testIPKey = "reg:min:192.0.2.1"
	for i := 0; i < regMinLimit; i++ {
		allowed, err := h.limiter.Allow(ctx, testIPKey, regMinLimit, regMinWindow)
		require.NoError(t, err)
		require.True(t, allowed, "pre-seed hit %d must be inside the window", i)
	}

	// A fully-valid POST (nonce + CSRF) still hits the exhausted window.
	gw := httptest.NewRecorder()
	h.get(gw, httptest.NewRequest(http.MethodGet, adminBasePath+"/register", nil))
	var nonce *http.Cookie
	for _, c := range gw.Result().Cookies() {
		if c.Name == regNonceCookie {
			nonce = c
		}
	}
	require.NotNil(t, nonce)
	tok := csrf.Issue(h.csrfKey, nonce.Value, csrf.DefaultTTL)

	form := url.Values{"email": {"throttled@t.example"}, "password": {"long-enough-pw"}}
	form.Set(csrf.FormField, tok)
	req := httptest.NewRequest(http.MethodPost, adminBasePath+"/register", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(nonce)
	w := httptest.NewRecorder()
	h.post(w, req)

	require.Equal(t, http.StatusTooManyRequests, w.Code, "6th in-window POST must be throttled")
	require.NotEmpty(t, w.Header().Get("Retry-After"))
	assert.Equal(t, 0, countAccounts(t, pool, "throttled@t.example"))
}

// TestRegister_HMACAbsent — spec leg register_hmac_absent: under
// AUTH_DRIVER=hmac the route is not mounted; the URL falls through to the
// panel catch-all → redirect/404, never a 200.
func TestRegister_HMACAbsent(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	dbtest.RequireTestDB(t, dsn)
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	ctx := context.Background()
	dbtest.DropAccountTables(t, pool)
	acctStore, op, err := accounts.Bootstrap(ctx, pool,
		accounts.OperatorSeed{Email: "admin-hmac@t.example", Password: "admin-pass-12345"})
	require.NoError(t, err)
	require.NotNil(t, op)

	t.Setenv("AUTH_DRIVER", "hmac")
	t.Setenv("ADMIN_HMAC_KEY", testHMACKey)
	t.Setenv("ADMIN_PASSWORD", "hmac-pw")
	t.Setenv("ADMIN_TOTP_ENC_KEY", strings.Repeat("ab", 32))
	t.Setenv("ADMIN_CSRF_KEY", strings.Repeat("cd", 32))

	handler, _, ok := New(hunt.NewStore(pool), applications.New(nil, t.TempDir(), uuid.MustParse(op.ID)), acctStore, accounts.NewKeyStore(pool), op.ID)
	require.True(t, ok, "hmac driver must construct")

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, adminBasePath+"/register", nil))
	require.NotEqual(t, http.StatusOK, w.Code,
		"GET /admin/register under hmac must not serve the form (404 or login-redirect)")
}

// TestPendingLogin_Hint — spec leg pending_login: an inactive passworded
// account with the CORRECT password still gets 401 + no session cookie, and
// the hint text explains the pending state (the upstream LoginFailHint seam).
func TestPendingLogin_Hint(t *testing.T) {
	f := newSelfServeFixture(t)
	ctx := context.Background()

	hash, err := auth.HashPassword("pending-pw-123")
	require.NoError(t, err)
	created, err := accounts.RegisterPending(ctx, f.pool, "pending@t.example", "P", hash)
	require.NoError(t, err)
	require.True(t, created)

	form := url.Values{"email": {"pending@t.example"}, "password": {"pending-pw-123"}}
	req := httptest.NewRequest(http.MethodPost, adminBasePath+"/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code, "a pending account must not log in")
	for _, c := range w.Result().Cookies() {
		require.NotEqual(t, "panel_admin", c.Name, "no session cookie may be issued")
	}
	require.Contains(t, w.Body.String(), "not yet active",
		"the pending hint must explain why login failed (LoginFailHint seam)")
}

// ─── keys legs ────────────────────────────────────────────────────────────

// TestKeys_MintScope — spec leg keys_mint_scope: B's mint produces a row
// owned by B; the raw token appears in the mint response and NOWHERE in a
// subsequent GET /admin/keys/.
func TestKeys_MintScope(t *testing.T) {
	f := newSelfServeFixture(t)
	ctx := context.Background()

	bid := newUserAccount(t, f.pool, "keyuser-b@t.example", "b-pass-12345")
	bCookies := selfServeLogin(t, f.handler, "keyuser-b@t.example", "b-pass-12345")

	w := selfServePost(t, f.handler, bCookies, f.csrfKey, adminBasePath+"/keys/mint", url.Values{
		"label": {"b-key"}, "current_password": {"b-pass-12345"}})
	require.Equal(t, http.StatusOK, w.Code, "mint renders the token page in place")
	tokenRe := regexp.MustCompile(`gj_[A-Za-z0-9_\-]+`)
	token := tokenRe.FindString(w.Body.String())
	require.NotEmpty(t, token, "mint response must contain the raw token")

	var owner uuid.UUID
	require.NoError(t, f.pool.QueryRow(ctx,
		`SELECT account_id FROM mcp_api_keys WHERE key_prefix = $1`, accounts.KeyPrefix(token)).Scan(&owner))
	assert.Equal(t, bid, owner, "minted key must belong to the caller")

	w = selfServeGet(f.handler, bCookies, adminBasePath+"/keys/")
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), accounts.KeyPrefix(token), "the key's prefix is listed")
	require.NotContains(t, w.Body.String(), token, "the raw token must never appear on the keys page")
}

// TestKeys_RevokeForeign — spec leg keys_revoke_foreign: B POSTs revoke on
// A's key id → the "not found or already revoked" answer, revoked_at stays
// NULL, and A's key still verifies through the real bearer path.
func TestKeys_RevokeForeign(t *testing.T) {
	f := newSelfServeFixture(t)
	ctx := context.Background()

	aid := newUserAccount(t, f.pool, "keyuser-a@t.example", "a-pass-12345")
	newUserAccount(t, f.pool, "keyuser-b@t.example", "b-pass-12345")
	bCookies := selfServeLogin(t, f.handler, "keyuser-b@t.example", "b-pass-12345")

	tokA, err := f.keyStore.Mint(ctx, aid, "a-key")
	require.NoError(t, err)
	var keyAID uuid.UUID
	require.NoError(t, f.pool.QueryRow(ctx,
		`SELECT id FROM mcp_api_keys WHERE key_prefix = $1`, accounts.KeyPrefix(tokA)).Scan(&keyAID))

	w := selfServePost(t, f.handler, bCookies, f.csrfKey,
		adminBasePath+"/keys/"+keyAID.String()+"/revoke", nil)
	require.Equal(t, http.StatusOK, w.Code, "foreign revoke re-renders the page with the message")
	require.Contains(t, w.Body.String(), "not found or already revoked")

	var revokedAt *time.Time
	require.NoError(t, f.pool.QueryRow(ctx,
		`SELECT revoked_at FROM mcp_api_keys WHERE id = $1`, keyAID).Scan(&revokedAt))
	require.Nil(t, revokedAt, "foreign key must stay unrevoked")

	_, verr := f.keyStore.Verifier()(ctx, tokA, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	require.NoError(t, verr, "the victim's key must still verify")
}

// TestKeys_Unauth — spec leg keys_unauth: no session → the MountAction guard
// rejects (redirect to login), zero rows inserted.
func TestKeys_Unauth(t *testing.T) {
	f := newSelfServeFixture(t)

	req := httptest.NewRequest(http.MethodPost, adminBasePath+"/keys/mint",
		strings.NewReader(url.Values{"label": {"x"}, "current_password": {"y"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusSeeOther, w.Code, "unauthenticated mint must redirect to login")
	require.Contains(t, w.Header().Get("Location"), "/login")

	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM mcp_api_keys`).Scan(&n))
	assert.Zero(t, n, "unauthenticated mint must insert nothing")
}

// TestKeys_ListScope — spec leg keys_list_scope: B's keys page shows B's
// prefix and never A's.
func TestKeys_ListScope(t *testing.T) {
	f := newSelfServeFixture(t)
	ctx := context.Background()

	aid := newUserAccount(t, f.pool, "keyuser-a@t.example", "a-pass-12345")
	bid := newUserAccount(t, f.pool, "keyuser-b@t.example", "b-pass-12345")
	bCookies := selfServeLogin(t, f.handler, "keyuser-b@t.example", "b-pass-12345")

	tokA, err := f.keyStore.Mint(ctx, aid, "a-key")
	require.NoError(t, err)
	tokB, err := f.keyStore.Mint(ctx, bid, "b-key")
	require.NoError(t, err)

	w := selfServeGet(f.handler, bCookies, adminBasePath+"/keys/")
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), accounts.KeyPrefix(tokB), "B sees its own prefix")
	require.NotContains(t, w.Body.String(), accounts.KeyPrefix(tokA), "B must never see A's prefix")
}

// TestKeys_MintStepUp — spec leg mint_stepup: wrong/absent current_password
// mints nothing; the correct password mints. Plus the MEDIUM-1 limiter: the
// 6th in-window attempt is 429'd BEFORE any password verify work.
func TestKeys_MintStepUp(t *testing.T) {
	f := newSelfServeFixture(t)
	ctx := context.Background()

	bid := newUserAccount(t, f.pool, "keyuser-b@t.example", "b-pass-12345")
	bCookies := selfServeLogin(t, f.handler, "keyuser-b@t.example", "b-pass-12345")

	// Absent password → no mint.
	w := selfServePost(t, f.handler, bCookies, f.csrfKey, adminBasePath+"/keys/mint", url.Values{
		"label": {"l1"}})
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "Incorrect password")

	// Wrong password → no mint.
	w = selfServePost(t, f.handler, bCookies, f.csrfKey, adminBasePath+"/keys/mint", url.Values{
		"label": {"l1"}, "current_password": {"wrong-pw"}})
	require.Contains(t, w.Body.String(), "Incorrect password")

	var n int
	require.NoError(t, f.pool.QueryRow(ctx,
		`SELECT count(*) FROM mcp_api_keys WHERE account_id = $1`, bid).Scan(&n))
	assert.Zero(t, n, "failed step-up must mint nothing")

	// Correct password → mint lands.
	w = selfServePost(t, f.handler, bCookies, f.csrfKey, adminBasePath+"/keys/mint", url.Values{
		"label": {"l1"}, "current_password": {"b-pass-12345"}})
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "gj_", "successful mint renders the token")

	// MEDIUM-1: burn the remaining limiter budget (5/min per account), then
	// the next attempt — even with the CORRECT password — is 429'd before
	// the bcrypt verify runs.
	for i := 0; i < 3; i++ { // 3 used above + 2 here = 5 total in-window
		selfServePost(t, f.handler, bCookies, f.csrfKey, adminBasePath+"/keys/mint", url.Values{
			"label": {"spam"}, "current_password": {"wrong-pw"}})
	}
	w = selfServePost(t, f.handler, bCookies, f.csrfKey, adminBasePath+"/keys/mint", url.Values{
		"label": {"l2"}, "current_password": {"b-pass-12345"}})
	require.Equal(t, http.StatusTooManyRequests, w.Code,
		"the per-account mint limiter must deny the 6th in-window attempt")
	require.NotEmpty(t, w.Header().Get("Retry-After"))
}

// ─── accounts legs ────────────────────────────────────────────────────────

// TestAccounts_RoleGate — spec leg accounts_role_gate: a role='user' session
// gets 403 on both the page and the activate action (with a VALID csrf so
// the in-handler role check — not the CSRF gate — is what denies).
// Mutation contract: deleting the sess.Role=="admin" check turns this RED.
func TestAccounts_RoleGate(t *testing.T) {
	f := newSelfServeFixture(t)
	ctx := context.Background()

	newUserAccount(t, f.pool, "plain-user@t.example", "u-pass-12345")
	uCookies := selfServeLogin(t, f.handler, "plain-user@t.example", "u-pass-12345")

	w := selfServeGet(f.handler, uCookies, adminBasePath+"/accounts/")
	require.Equal(t, http.StatusForbidden, w.Code, "non-admin must not read the accounts page")

	// A pending target the user tries to activate.
	hash, err := auth.HashPassword("pend-pw-1234")
	require.NoError(t, err)
	_, err = accounts.RegisterPending(ctx, f.pool, "target@t.example", "T", hash)
	require.NoError(t, err)
	var targetID uuid.UUID
	require.NoError(t, f.pool.QueryRow(ctx,
		`SELECT id FROM panel_accounts WHERE email = 'target@t.example'`).Scan(&targetID))

	w = selfServePost(t, f.handler, uCookies, f.csrfKey,
		adminBasePath+"/accounts/"+targetID.String()+"/activate", nil)
	require.Equal(t, http.StatusForbidden, w.Code, "non-admin activate must 403")

	var active bool
	require.NoError(t, f.pool.QueryRow(ctx,
		`SELECT active FROM panel_accounts WHERE id = $1`, targetID).Scan(&active))
	assert.False(t, active, "a 403'd activate must leave the row inactive")
}

// TestAccounts_SelfDeactivate — spec leg accounts_self_deactivate: admin
// POSTs its own id to deactivate → 400, the row stays active.
func TestAccounts_SelfDeactivate(t *testing.T) {
	f := newSelfServeFixture(t)
	adminCookies := selfServeLogin(t, f.handler, "admin-selfserve@t.example", "admin-pass-12345")

	w := selfServePost(t, f.handler, adminCookies, f.csrfKey,
		adminBasePath+"/accounts/"+f.op.ID+"/deactivate", nil)
	require.Equal(t, http.StatusBadRequest, w.Code, "self-deactivate must be refused")

	var active bool
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`SELECT active FROM panel_accounts WHERE id = $1`, f.op.ID).Scan(&active))
	assert.True(t, active, "self-deactivate attempt must leave the admin active")
}

// TestAccounts_AdminActivate — the happy counterpart of the role gate: an
// admin activates a pending account and it becomes loginable.
func TestAccounts_AdminActivate(t *testing.T) {
	f := newSelfServeFixture(t)
	ctx := context.Background()

	hash, err := auth.HashPassword("pend-pw-1234")
	require.NoError(t, err)
	_, err = accounts.RegisterPending(ctx, f.pool, "activatable@t.example", "T", hash)
	require.NoError(t, err)
	var targetID uuid.UUID
	require.NoError(t, f.pool.QueryRow(ctx,
		`SELECT id FROM panel_accounts WHERE email = 'activatable@t.example'`).Scan(&targetID))

	// Admin reads the page fine (200 — the role gate is not a blanket deny).
	adminCookies := selfServeLogin(t, f.handler, "admin-selfserve@t.example", "admin-pass-12345")
	w := selfServeGet(f.handler, adminCookies, adminBasePath+"/accounts/")
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "activatable@t.example")

	w = selfServePost(t, f.handler, adminCookies, f.csrfKey,
		adminBasePath+"/accounts/"+targetID.String()+"/activate", nil)
	require.Equal(t, http.StatusSeeOther, w.Code, "activate redirects back to the list")

	var active bool
	require.NoError(t, f.pool.QueryRow(ctx,
		`SELECT active FROM panel_accounts WHERE id = $1`, targetID).Scan(&active))
	require.True(t, active)

	// And the account can now log in for real.
	selfServeLogin(t, f.handler, "activatable@t.example", "pend-pw-1234")
}
