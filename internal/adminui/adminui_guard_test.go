package adminui

import (
	"net/http"
	"testing"
)

// stubAuthNoCookieName is a minimal auth.Authenticator stub that intentionally
// does NOT implement cookieNamer (no SessionCookieName method). Used to verify
// the fail-closed guard in checkAuthCapabilities fires for non-conforming impls.
type stubAuthNoCookieName struct{}

func (stubAuthNoCookieName) Verified(*http.Request) bool                    { return false }
func (stubAuthNoCookieName) LoginHandler() http.Handler                     { return http.NotFoundHandler() }
func (stubAuthNoCookieName) LogoutHandler() http.Handler                    { return http.NotFoundHandler() }
func (stubAuthNoCookieName) Require(next http.HandlerFunc) http.HandlerFunc { return next }

// TestCheckAuthCapabilities_PanicsWithoutCookieNamer verifies that
// checkAuthCapabilities panics with the fail-closed diagnostic when an
// auth.Authenticator that does not implement SessionCookieName() is passed in.
//
// Red-on-revert: removing or weakening checkAuthCapabilities causes this test
// to fail (no panic → recover catches nothing → t.Fatal fires).
func TestCheckAuthCapabilities_PanicsWithoutCookieNamer(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("checkAuthCapabilities: expected panic for authenticator lacking SessionCookieName(), got none")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("checkAuthCapabilities: panic value is not a string: %v", r)
		}
		want := "adminui: authenticator must implement SessionCookieName() — CSRF session binding fail-closed"
		if msg != want {
			t.Fatalf("checkAuthCapabilities: panic message = %q; want %q", msg, want)
		}
	}()
	checkAuthCapabilities(stubAuthNoCookieName{})
}
