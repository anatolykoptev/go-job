package adminui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/anatolykoptev/go-panel/csrf"
)

// TestRescoreHandler_CSRFReject verifies that a missing CSRF token returns 403
// before any DB call is made.
// RED-on-revert: remove the csrf.Verify call in rescoreHandler → 400 or 500.
func TestRescoreHandler_CSRFReject(t *testing.T) {
	key := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") // 32 bytes
	a := buildTestAuth(key)
	// nil pool + nil store — expect 403 before any DB access.
	handler := rescoreHandler(nil, nil, a, key)

	form := url.Values{}
	// _csrf intentionally omitted → should get 403.
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/admin/jobs/1/rescore",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "1")

	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("want 403 for missing CSRF, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestRescoreHandler_CSRFExpired verifies that an expired CSRF token returns 403.
func TestRescoreHandler_CSRFExpired(t *testing.T) {
	key := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	a := buildTestAuth(key)
	handler := rescoreHandler(nil, nil, a, key)

	// Forge a token with expiry=1 (Jan 1 1970) to guarantee it is expired.
	expiredToken := "1|invalidmac"

	form := url.Values{}
	form.Set(csrf.FormField, expiredToken)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/admin/jobs/1/rescore",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "1")

	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("want 403 for expired CSRF, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestRescoreHandler_BadID verifies that a non-numeric id returns 400
// before CSRF validation or any DB call.
func TestRescoreHandler_BadID(t *testing.T) {
	key := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	a := buildTestAuth(key)
	handler := rescoreHandler(nil, nil, a, key)

	form := url.Values{}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/admin/jobs/abc/rescore",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "abc")

	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400 for non-numeric id, got %d", rr.Code)
	}
}
