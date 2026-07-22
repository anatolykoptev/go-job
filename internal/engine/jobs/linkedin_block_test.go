package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	linkedin "github.com/anatolykoptev/go-linkedin"
	"github.com/anatolykoptev/go-twitter/social"
	"github.com/anatolykoptev/go_job/internal/engine"
)

func TestClassifyLinkedInResponse(t *testing.T) {
	cleanJobResults := []byte(`<ul><li><div class="base-card"><a class="base-card__full-link" href="https://www.linkedin.com/jobs/view/4335742219"><span class="sr-only">Go Developer</span></a><h3 class="base-search-card__title">Go Developer</h3><h4 class="base-search-card__subtitle">Acme</h4><div class="job-search-card__location">SF</div></div></li></ul>`)

	tests := []struct {
		name   string
		status int
		body   []byte
		want   linkedInBlockKind
	}{
		// Status-first classification (3xx/4xx/5xx/999).
		{"302 authwall redirect", 302, []byte(`<html><body><a href="/checkpoint/authwall">Sign in</a></body></html>`), liChallenge},
		{"302 no body (Voyager error path)", 302, nil, liChallenge},
		{"401 unauthorized", 401, nil, liHardBlock},
		{"403 forbidden", 403, nil, liHardBlock},
		{"429 rate limited", 429, nil, liRateLimited},
		{"999 LinkedIn block", 999, nil, liHardBlock},

		// Unhandled 4xx/5xx MUST escalate as hard blocks (issue #291: default
		// returning liOK misclassified error pages as success and short-circuited
		// the cascade, defeating the 429/503-storm breaker).
		{"404 not found", 404, nil, liHardBlock},
		{"500 server error", 500, nil, liHardBlock},
		{"502 bad gateway", 502, nil, liHardBlock},
		{"503 service unavailable", 503, nil, liHardBlock},

		// Non-302 3xx redirects → challenge (LinkedIn redirects to authwall/checkpoint).
		{"301 permanent redirect", 301, nil, liChallenge},
		{"308 permanent redirect", 308, nil, liChallenge},

		// 2xx non-200 (e.g. 204 No Content) stays OK.
		{"204 no content", 204, nil, liOK},

		// 200 with challenge-body markers (case-insensitive).
		{"200 checkpoint body", 200, []byte(`<html><title>Security Verification | LinkedIn</title><body>checkpoint</body></html>`), liChallenge},
		{"200 authwall body", 200, []byte(`<html><div class="authwall">Join LinkedIn</div></html>`), liChallenge},
		{"200 challenge body", 200, []byte(`<html><body>Please complete a challenge</body></html>`), liChallenge},
		{"200 captcha body", 200, []byte(`<html><body>Enter the captcha below</body></html>`), liChallenge},
		{"200 security verification body", 200, []byte(`<html><body>Security verification required</body></html>`), liChallenge},
		{"200 markers uppercase", 200, []byte(`<HTML><BODY>CAPTCHA</BODY></HTML>`), liChallenge},

		// 200 clean — no block markers.
		{"200 clean job results", 200, cleanJobResults, liOK},
		{"200 empty body", 200, nil, liOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyLinkedInResponse(tt.status, tt.body)
			if got != tt.want {
				t.Errorf("classifyLinkedInResponse(status=%d) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

func TestClassifyLinkedInError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want linkedInBlockKind
	}{
		{"nil error", nil, liOK},
		{"voyager 401 auth failed", &linkedin.VoyagerStatusError{Endpoint: "/identity/profile", Status: 401}, liHardBlock},
		{"voyager 403 auth failed", &linkedin.VoyagerStatusError{Endpoint: "/identity/profile", Status: 403}, liHardBlock},
		{"voyager 302 redirect", &linkedin.VoyagerStatusError{Endpoint: "/identity/profile", Status: 302}, liChallenge},
		{"voyager 429 rate limited", &linkedin.VoyagerStatusError{Endpoint: "/identity/profile", Status: 429}, liRateLimited},
		{"voyager 999 block", &linkedin.VoyagerStatusError{Endpoint: "/identity/profile", Status: 999}, liHardBlock},
		{"voyager HTML response (200 challenge)", &linkedin.VoyagerHTMLResponseError{Endpoint: "/identity/profile"}, liChallenge},
		{"voyager network error (no status)", errStr("voyager request /identity/profile: connection refused"), liOK},
		{"rate limiter (our own, not LinkedIn 429)", errStr("linkedin rate limit exhausted (0 remaining)"), liOK},
		// errors.As unwraps wrapped Voyager errors — the whole point over regex
		// (a regex on the wrapped string would still match here, but a wording
		// change in the wrapping fmt.Errorf would break regex, not errors.As).
		{"wrapped voyager 403 (errors.As unwraps)", fmt.Errorf("acquire: %w", &linkedin.VoyagerStatusError{Endpoint: "/identity/profile", Status: 403}), liHardBlock},
		{"wrapped voyager HTML challenge", fmt.Errorf("fetch profile: %w", &linkedin.VoyagerHTMLResponseError{Endpoint: "/identity/profile"}), liChallenge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyLinkedInError(tt.err)
			if got != tt.want {
				t.Errorf("classifyLinkedInError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestIsAuthErrorUsesClassifier(t *testing.T) {
	// Cases the OLD string-match caught — must still be auth errors.
	oldCaught := []error{
		&linkedin.VoyagerStatusError{Endpoint: "/identity/profile", Status: 401},
		&linkedin.VoyagerStatusError{Endpoint: "/identity/profile", Status: 403},
		&linkedin.VoyagerStatusError{Endpoint: "/identity/profile", Status: 302},
	}
	for _, e := range oldCaught {
		if !isAuthError(e) {
			t.Errorf("isAuthError(%q) = false, want true (preserved from old logic)", e)
		}
	}

	// Cases the OLD string-match MISSED — must now be auth errors.
	// (429 is NOT here — a rate limit must NOT rotate; see TestIsAuthErrorNoRotateOnRateLimit.)
	newlyCaught := []error{
		&linkedin.VoyagerStatusError{Endpoint: "/identity/profile", Status: 999},
		&linkedin.VoyagerHTMLResponseError{Endpoint: "/identity/profile"},
	}
	for _, e := range newlyCaught {
		if !isAuthError(e) {
			t.Errorf("isAuthError(%q) = false, want true (newly caught by classifier)", e)
		}
	}

	// Non-auth errors must NOT trigger rotation.
	notAuth := []error{
		nil,
		errStr("linkedin rate limit exhausted (0 remaining)"),
		errStr("voyager request /identity/profile: connection refused"),
		// 429 is a transient rate limit, NOT an auth/block signal — rotating
		// would poison a healthy account's go-social health signal and retry
		// immediately against a rate-limited endpoint with no backoff (issue #291).
		&linkedin.VoyagerStatusError{Endpoint: "/identity/profile", Status: 429},
	}
	for _, e := range notAuth {
		if isAuthError(e) {
			t.Errorf("isAuthError(%q) = true, want false", e)
		}
	}
}

type errStr string

func (e errStr) Error() string { return string(e) }

// TestIsAuthErrorNoRotateOnRateLimit verifies that a 429 rate limit does NOT
// invalidate the pooled client or report auth_error to go-social. The pool's
// expiresAt must remain unchanged and ReportUsage must not be called.
//
// Regression guard for issue #291: the old isAuthError (= "classifier != liOK")
// treated 429 as an auth error, poisoning a healthy account's health signal and
// retrying immediately against a rate-limited endpoint with no backoff.
func TestIsAuthErrorNoRotateOnRateLimit(t *testing.T) {
	var reportCalls atomic.Int32
	socialSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			reportCalls.Add(1)
			w.WriteHeader(http.StatusOK)
			return
		}
		// AcquireAccount fallback (should not be called — pool is primed).
		_ = json.NewEncoder(w).Encode(social.Credentials{
			ID:          "test-id",
			Credentials: map[string]string{"auth_token": "t", "ct0": "c"},
		})
	}))
	defer socialSrv.Close()

	engine.Cfg.SocialClient = social.NewClient(socialSrv.URL, "tok", "go-job")
	t.Cleanup(func() { engine.Cfg.SocialClient = nil })

	// Prime the pool with a real client and a future expiry so getLinkedInClient
	// takes the fast path (no social AcquireAccount call).
	client, err := linkedin.New(linkedin.ClientConfig{Cookies: map[string]string{"auth_token": "t", "ct0": "c"}})
	if err != nil {
		t.Fatalf("linkedin.New: %v", err)
	}
	linkedinPool.mu.Lock()
	linkedinPool.client.Store(client)
	linkedinPool.accountID = "test-id"
	linkedinPool.refreshedAt = time.Now()
	expires := time.Now().Add(10 * time.Minute)
	linkedinPool.expiresAt = expires
	linkedinPool.mu.Unlock()
	t.Cleanup(func() {
		linkedinPool.mu.Lock()
		linkedinPool.client.Store(nil)
		linkedinPool.expiresAt = time.Time{}
		linkedinPool.mu.Unlock()
	})

	// withRetry: fn returns a 429-classified error. isAuthError must be false →
	// no invalidate, no reportLinkedInAuthError, no retry.
	rateLimitErr := &linkedin.VoyagerStatusError{Endpoint: "/identity/profile", Status: 429}
	_, callErr := withRetry[any](context.Background(), func(*linkedin.Client) (any, error) {
		return nil, rateLimitErr
	})
	if !errors.Is(callErr, rateLimitErr) {
		t.Fatalf("withRetry returned err = %v, want the original 429 error unchanged", callErr)
	}

	// Pool expiry MUST be unchanged (invalidate was not called).
	linkedinPool.mu.Lock()
	gotExpires := linkedinPool.expiresAt
	linkedinPool.mu.Unlock()
	if !gotExpires.Equal(expires) {
		t.Errorf("pool expiresAt changed after 429: got %v, want %v (invalidate must NOT run on rate limit)", gotExpires, expires)
	}

	// go-social ReportUsage MUST NOT be called (no auth_error report).
	if n := reportCalls.Load(); n != 0 {
		t.Errorf("go-social ReportUsage called %d times on 429 path, want 0 (no auth_error report on rate limit)", n)
	}
}

// TestIsAuthErrorRotatesOnHardBlock verifies the rotation path IS preserved for
// real auth/block signals (liHardBlock / liChallenge) — the fix to isAuthError
// must not over-correct and suppress rotation for genuine blocks.
func TestIsAuthErrorRotatesOnHardBlock(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"401 hard block", &linkedin.VoyagerStatusError{Endpoint: "/identity/profile", Status: 401}, true},
		{"403 hard block", &linkedin.VoyagerStatusError{Endpoint: "/identity/profile", Status: 403}, true},
		{"302 challenge", &linkedin.VoyagerStatusError{Endpoint: "/identity/profile", Status: 302}, true},
		{"999 hard block", &linkedin.VoyagerStatusError{Endpoint: "/identity/profile", Status: 999}, true},
		{"200 challenge body", &linkedin.VoyagerHTMLResponseError{Endpoint: "/identity/profile"}, true},
		{"429 rate limit (no rotate)", &linkedin.VoyagerStatusError{Endpoint: "/identity/profile", Status: 429}, false},
		{"network error (no rotate)", errStr("voyager request /identity/profile: connection refused"), false},
		{"nil (no rotate)", nil, false},
		// Wrapped Voyager error must still rotate via errors.As unwrapping.
		{"wrapped 403 hard block", fmt.Errorf("acquire: %w", &linkedin.VoyagerStatusError{Endpoint: "/identity/profile", Status: 403}), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isAuthError(c.err); got != c.want {
				t.Errorf("isAuthError(%q) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}
