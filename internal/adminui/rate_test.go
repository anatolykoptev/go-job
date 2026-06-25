package adminui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/go-panel/auth"
	"github.com/anatolykoptev/go-panel/csrf"
)

// TestValidHuntStages_Allowlist is a fast table check; no DB needed.
// Red-on-revert: remove validHuntStages or change entries → mismatches here.
func TestValidHuntStages_Allowlist(t *testing.T) {
	cases := []struct {
		stage string
		want  bool
	}{
		{"new", true},
		{"interesting", true},
		{"saved", true},
		{"discarded", true},
		{"claimed", true},
		{"hacked", false},
		{"applied", false},
		{"offer", false},
		{"", false},
		{"NEW", false},
	}
	for _, tc := range cases {
		got := validHuntStages[tc.stage]
		if got != tc.want {
			t.Errorf("validHuntStages[%q] = %v, want %v", tc.stage, got, tc.want)
		}
	}
}

// TestRateHandler_CSRFReject verifies that a missing CSRF token returns 403.
// Red-on-revert: remove the csrf.Verify call in rateHandler → returns 400 or 500.
func TestRateHandler_CSRFReject(t *testing.T) {
	key := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") // 32 bytes
	a := buildTestAuth(key)
	// nil pool — we expect 403 before any DB call.
	handler := rateHandler(nil, a, key)

	form := url.Values{}
	form.Set("stage", "saved")
	// _csrf intentionally omitted → should get 403.
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/admin/jobs/1/rate",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "1")

	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("want 403 for missing CSRF, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestRateHandler_CSRFExpired verifies that an expired CSRF token returns 403.
func TestRateHandler_CSRFExpired(t *testing.T) {
	key := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	a := buildTestAuth(key)
	handler := rateHandler(nil, a, key)

	// Forge a token with expiry=1 (Jan 1 1970) to guarantee it's expired.
	expiredToken := "1|invalidmac"

	form := url.Values{}
	form.Set("stage", "saved")
	form.Set(csrf.FormField, expiredToken)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/admin/jobs/1/rate",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "1")

	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("want 403 for expired CSRF, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestRateHandler_BadID verifies that a non-numeric id returns 400.
func TestRateHandler_BadID(t *testing.T) {
	key := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	a := buildTestAuth(key)
	handler := rateHandler(nil, a, key)

	form := url.Values{}
	form.Set("stage", "saved")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/admin/jobs/abc/rate",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "abc")

	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400 for bad id, got %d", rr.Code)
	}
}

// buildTestAuth returns a minimal *auth.HMACAuth for handler tests.
func buildTestAuth(hmacKey []byte) *auth.HMACAuth {
	return auth.NewHMACAuth(auth.HMACConfig{
		Username:   "admin",
		Password:   "pw",
		HMACKey:    hmacKey,
		BasePath:   adminBasePath,
		SessionTTL: time.Hour,
	})
}
