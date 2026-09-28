package adminui

// password_test.go — deny/behavior legs for the self-serve password change
// surface (bcrypt driver only).

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPasswordChange_HappyPath: current+new+confirm → "Password updated.",
// the NEW password logs in, the OLD one no longer does.
func TestPasswordChange_HappyPath(t *testing.T) {
	f := newSelfServeFixture(t)
	newUserAccount(t, f.pool, "pw-user@t.example", "old-pass-12345")
	cookies := selfServeLogin(t, f.handler, "pw-user@t.example", "old-pass-12345")

	w := selfServePost(t, f.handler, cookies, f.csrfKey, adminBasePath+"/password/change", url.Values{
		"current_password": {"old-pass-12345"},
		"new_password":     {"new-pass-67890"},
		"confirm_password": {"new-pass-67890"},
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "Password updated.")

	// New credential logs in; the old one is dead.
	selfServeLogin(t, f.handler, "pw-user@t.example", "new-pass-67890")
	oldLogin := httptestLogin(t, f.handler, "pw-user@t.example", "old-pass-12345")
	assert.Equal(t, http.StatusUnauthorized, oldLogin.Code, "old password must stop working after change")
}

// TestPasswordChange_WrongCurrent: a bad step-up password re-renders the
// form with a generic error and leaves the hash untouched (old password
// still logs in).
func TestPasswordChange_WrongCurrent(t *testing.T) {
	f := newSelfServeFixture(t)
	newUserAccount(t, f.pool, "pw-wrong@t.example", "real-pass-12345")
	cookies := selfServeLogin(t, f.handler, "pw-wrong@t.example", "real-pass-12345")

	w := selfServePost(t, f.handler, cookies, f.csrfKey, adminBasePath+"/password/change", url.Values{
		"current_password": {"not-the-password"},
		"new_password":     {"new-pass-67890"},
		"confirm_password": {"new-pass-67890"},
	})
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Incorrect current password")
	selfServeLogin(t, f.handler, "pw-wrong@t.example", "real-pass-12345")
}

// TestPasswordChange_Validation: too-short and mismatched-confirm new
// passwords fail BEFORE any bcrypt/DB work — the stored hash is unchanged.
func TestPasswordChange_Validation(t *testing.T) {
	f := newSelfServeFixture(t)
	newUserAccount(t, f.pool, "pw-val@t.example", "keep-pass-12345")
	cookies := selfServeLogin(t, f.handler, "pw-val@t.example", "keep-pass-12345")

	for name, fields := range map[string]url.Values{
		"short":    {"current_password": {"keep-pass-12345"}, "new_password": {"short"}, "confirm_password": {"short"}},
		"mismatch": {"current_password": {"keep-pass-12345"}, "new_password": {"new-pass-67890"}, "confirm_password": {"different-67890"}},
	} {
		w := selfServePost(t, f.handler, cookies, f.csrfKey, adminBasePath+"/password/change", fields)
		require.Equal(t, http.StatusOK, w.Code, name)
		assert.NotContains(t, w.Body.String(), "Password updated", name)
	}
	selfServeLogin(t, f.handler, "pw-val@t.example", "keep-pass-12345")
}

// TestPasswordChange_StepUpThrottled: the pw:<account> limiter caps step-up
// verifies — repeated wrong-current-password attempts hit 429, so a stolen
// session cannot grind passwords. Asserts the REAL bound, not a test seam.
func TestPasswordChange_StepUpThrottled(t *testing.T) {
	f := newSelfServeFixture(t)
	newUserAccount(t, f.pool, "pw-throttle@t.example", "real-pass-12345")
	cookies := selfServeLogin(t, f.handler, "pw-throttle@t.example", "real-pass-12345")

	var last *httptest.ResponseRecorder
	for i := 0; i < pwRateLimit+1; i++ {
		last = selfServePost(t, f.handler, cookies, f.csrfKey, adminBasePath+"/password/change", url.Values{
			"current_password": {"guess-" + string(rune('a'+i))},
			"new_password":     {"new-pass-67890"},
			"confirm_password": {"new-pass-67890"},
		})
	}
	require.Equal(t, http.StatusTooManyRequests, last.Code, "attempt %d must be throttled", pwRateLimit+1)
	assert.NotEmpty(t, last.Header().Get("Retry-After"))
	selfServeLogin(t, f.handler, "pw-throttle@t.example", "real-pass-12345")
}

// TestPasswordChange_Unauth: no session → the MountAction guard rejects
// before the handler; nothing changes.
func TestPasswordChange_Unauth(t *testing.T) {
	f := newSelfServeFixture(t)
	newUserAccount(t, f.pool, "pw-unauth@t.example", "keep-pass-12345")

	req := httptest.NewRequest(http.MethodPost, adminBasePath+"/password/change",
		strings.NewReader(url.Values{"current_password": {"keep-pass-12345"}, "new_password": {"new-pass-67890"}, "confirm_password": {"new-pass-67890"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	assert.NotEqual(t, http.StatusOK, w.Code, "unguarded POST must never reach the handler")
	selfServeLogin(t, f.handler, "pw-unauth@t.example", "keep-pass-12345")
}

// httptestLogin posts the login form and returns the raw recorder — unlike
// selfServeLogin it does not require success.
func httptestLogin(t *testing.T, h http.Handler, email, pw string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"email": {email}, "password": {pw}}
	req := httptest.NewRequest(http.MethodPost, adminBasePath+"/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}
