package jobs

import (
	"errors"
	"fmt"
	"testing"

	stealth "github.com/anatolykoptev/go-stealth"
)

// TestProxyErrStatus verifies that proxyErrStatus extracts the upstream HTTP
// status from the typed error returned by engine.FetchProxyBody's underlying
// fetcher (go-engine fetch.HttpStatusError, which is a type alias for
// stealth.HttpStatusError — both tiers return the SAME type). A status-carrying
// error yields its real StatusCode so the cascade classifier sees the true code
// (429→liRateLimited, 403/999→liHardBlock); a genuine transport/network error
// (no typed status) yields 0 → liNetworkError.
//
// Regression guard for issue #307: before this helper, linkedInTierAProxy
// hardcoded status 0 on every error, so a persistent 429 storm alerted as
// "network-down" (liNetworkError) instead of "rate-limited" (liRateLimited),
// partially regressing #291's discrimination.
func TestProxyErrStatus(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"direct-tier 429", &stealth.HttpStatusError{StatusCode: 429}, 429},
		{"proxy-tier 403", &stealth.HttpStatusError{StatusCode: 403}, 403},
		{"linkedin 999 hard block", &stealth.HttpStatusError{StatusCode: 999}, 999},
		{"wrapped 999 unwraps via errors.As", fmt.Errorf("fetch failed: %w", &stealth.HttpStatusError{StatusCode: 999}), 999},
		{"double-wrapped 429 unwraps", fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", &stealth.HttpStatusError{StatusCode: 429})), 429},
		{"plain network error → 0", errors.New("connection reset by peer"), 0},
		{"nil error → 0", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := proxyErrStatus(tt.err); got != tt.want {
				t.Errorf("proxyErrStatus(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}
