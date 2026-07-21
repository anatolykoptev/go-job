package jobs

import (
	"testing"
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
		{"500 server error (unclassified)", 500, nil, liOK},

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
		{"voyager 401 auth failed", errStr("voyager auth failed: status 401 (cookies may be expired)"), liHardBlock},
		{"voyager 403 auth failed", errStr("voyager auth failed: status 403 (cookies may be expired)"), liHardBlock},
		{"voyager 302 redirect", errStr("voyager /identity/profile: status 302"), liChallenge},
		{"voyager 429 rate limited", errStr("voyager /identity/profile: status 429"), liRateLimited},
		{"voyager 999 block", errStr("voyager /identity/profile: status 999"), liHardBlock},
		{"voyager HTML response (200 challenge)", errStr("voyager auth failed: HTML response (session expired or IP blocked)"), liChallenge},
		{"voyager network error (no status)", errStr("voyager request /identity/profile: connection refused"), liOK},
		{"rate limiter (our own, not LinkedIn 429)", errStr("linkedin rate limit exhausted (0 remaining)"), liOK},
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
		errStr("voyager auth failed: status 401 (cookies may be expired)"),
		errStr("voyager auth failed: status 403 (cookies may be expired)"),
		errStr("voyager /identity/profile: status 302"),
	}
	for _, e := range oldCaught {
		if !isAuthError(e) {
			t.Errorf("isAuthError(%q) = false, want true (preserved from old logic)", e)
		}
	}

	// Cases the OLD string-match MISSED — must now be auth errors.
	newlyCaught := []error{
		errStr("voyager /identity/profile: status 429"),
		errStr("voyager /identity/profile: status 999"),
		errStr("voyager auth failed: HTML response (session expired or IP blocked)"),
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
	}
	for _, e := range notAuth {
		if isAuthError(e) {
			t.Errorf("isAuthError(%q) = true, want false", e)
		}
	}
}

type errStr string

func (e errStr) Error() string { return string(e) }
