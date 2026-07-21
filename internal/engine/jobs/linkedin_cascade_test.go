package jobs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anatolykoptev/go-kit/breaker"
	"github.com/anatolykoptev/go_job/internal/engine"
)

// withTiers swaps the two cascade tier functions and restores them on cleanup.
func withTiers(t *testing.T, tA, tB linkedInTierFunc) {
	t.Helper()
	origA, origB := linkedInTierAFetch, linkedInTierBFetch
	t.Cleanup(func() {
		linkedInTierAFetch = origA
		linkedInTierBFetch = origB
	})
	linkedInTierAFetch = tA
	linkedInTierBFetch = tB
}

// withBreaker swaps the package linkedinBreaker with a fresh test breaker and
// restores the original on cleanup. Isolates breaker state from other tests.
func withBreaker(t *testing.T, b *breaker.Breaker) {
	t.Helper()
	orig := linkedinBreaker
	t.Cleanup(func() { linkedinBreaker = orig })
	linkedinBreaker = b
}

func TestLinkedInCascadeEscalatesThroughTiers(t *testing.T) {
	var callsA, callsB atomic.Int32

	tierA := func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		callsA.Add(1)
		return 403, []byte(`forbidden`), nil // liHardBlock
	}
	tierB := func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		callsB.Add(1)
		return 200, []byte(`<html><body>clean job results page</body></html>`), nil // liOK
	}
	withTiers(t, tierA, tierB)

	body, err := linkedInRequest(context.Background(), "https://www.linkedin.com/jobs-guest/jobs/api/seeMoreJobPostings/search?keywords=go")
	if err != nil {
		t.Fatalf("linkedInRequest returned error: %v", err)
	}
	if string(body) != `<html><body>clean job results page</body></html>` {
		t.Errorf("expected tier-B body, got %q", string(body))
	}
	if got := callsA.Load(); got != 1 {
		t.Errorf("tierA call count = %d, want 1", got)
	}
	if got := callsB.Load(); got != 1 {
		t.Errorf("tierB call count = %d, want 1", got)
	}
}

func TestLinkedInCascadeShortCircuitsOnTierAOK(t *testing.T) {
	var callsA, callsB atomic.Int32

	tierA := func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		callsA.Add(1)
		return 200, []byte(`<html><body>clean results</body></html>`), nil // liOK
	}
	tierB := func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		callsB.Add(1)
		return 200, []byte(`should not be called`), nil
	}
	withTiers(t, tierA, tierB)

	body, err := linkedInRequest(context.Background(), "https://www.linkedin.com/jobs-guest/jobs/api/seeMoreJobPostings/search?keywords=go")
	if err != nil {
		t.Fatalf("linkedInRequest returned error: %v", err)
	}
	if string(body) != `<html><body>clean results</body></html>` {
		t.Errorf("expected tier-A body, got %q", string(body))
	}
	if got := callsA.Load(); got != 1 {
		t.Errorf("tierA call count = %d, want 1", got)
	}
	if got := callsB.Load(); got != 0 {
		t.Errorf("tierB call count = %d, want 0 (short-circuit)", got)
	}
}

func TestLinkedInCascadeAllTiersBlocked(t *testing.T) {
	var callsA, callsB atomic.Int32

	tierA := func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		callsA.Add(1)
		return 999, []byte(`blocked`), nil // liHardBlock
	}
	tierB := func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		callsB.Add(1)
		return 0, nil, errors.New("gowowa render: connection refused") // err
	}
	withTiers(t, tierA, tierB)

	_, err := linkedInRequest(context.Background(), "https://www.linkedin.com/jobs-guest/jobs/api/seeMoreJobPostings/search?keywords=go")
	if err == nil {
		t.Fatal("expected error when all tiers blocked, got nil")
	}
	if got := callsA.Load(); got != 1 {
		t.Errorf("tierA call count = %d, want 1", got)
	}
	if got := callsB.Load(); got != 1 {
		t.Errorf("tierB call count = %d, want 1", got)
	}
}

func TestLinkedInCascadeTierAErrorEscalates(t *testing.T) {
	// Tier A returns a network error (not a block signal) — cascade should escalate.
	var callsA, callsB atomic.Int32

	tierA := func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		callsA.Add(1)
		return 0, nil, errors.New("connection reset")
	}
	tierB := func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		callsB.Add(1)
		return 200, []byte(`<html><body>clean via render</body></html>`), nil // liOK
	}
	withTiers(t, tierA, tierB)

	body, err := linkedInRequest(context.Background(), "https://www.linkedin.com/jobs-guest/jobs/api/seeMoreJobPostings/search?keywords=go")
	if err != nil {
		t.Fatalf("linkedInRequest returned error: %v", err)
	}
	if string(body) != `<html><body>clean via render</body></html>` {
		t.Errorf("expected tier-B body, got %q", string(body))
	}
	if got := callsA.Load(); got != 1 {
		t.Errorf("tierA call count = %d, want 1", got)
	}
	if got := callsB.Load(); got != 1 {
		t.Errorf("tierB call count = %d, want 1", got)
	}
}

// TestLinkedInCascadeTierA_503Escalates verifies that a Tier-A 503 (err=nil)
// does NOT short-circuit the cascade as a false success. Regression guard for
// issue #291: the old classifier's `default: return liOK` misclassified 503
// error pages as success, recorded linkedinBreaker.Record(true) on the error
// page, and returned the error-page body to MCP tools.
//
// With the fix, 503 → liHardBlock → escalate to Tier-B (liOK) → return Tier-B
// body. The breaker records success ONCE for the cascade (at Tier-B), never on
// the 503.
func TestLinkedInCascadeTierA_503Escalates(t *testing.T) {
	var callsA, callsB atomic.Int32

	tierA := func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		callsA.Add(1)
		return 503, []byte(`<html><body>503 Service Unavailable</body></html>`), nil // err=nil, must NOT be liOK
	}
	tierB := func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		callsB.Add(1)
		return 200, []byte(`<html><body>clean job results</body></html>`), nil // liOK
	}
	withTiers(t, tierA, tierB)

	body, err := linkedInRequest(context.Background(), "https://www.linkedin.com/jobs-guest/jobs/api/seeMoreJobPostings/search?keywords=go")
	if err != nil {
		t.Fatalf("linkedInRequest returned error: %v (503 should escalate, not fail)", err)
	}
	if string(body) != `<html><body>clean job results</body></html>` {
		t.Errorf("expected tier-B clean body, got %q (503 short-circuited as false success)", string(body))
	}
	if got := callsA.Load(); got != 1 {
		t.Errorf("tierA call count = %d, want 1", got)
	}
	if got := callsB.Load(); got != 1 {
		t.Errorf("tierB call count = %d, want 1 (503 must escalate)", got)
	}
}

// TestLinkedInCascadeAllTiers503TripsBreaker verifies that when ALL tiers
// return 503 (err=nil), the cascade records linkedinBreaker.Record(false) on
// each call (NOT Record(true)), so the breaker trips after FailThreshold calls.
// This is the direct observation that 503 is not recorded as success.
//
// With the old `default: return liOK` bug, 503 → liOK → Record(true) at Tier-A
// → the breaker would NEVER trip on a 503 storm (it records success). The fix
// classifies 503 as liHardBlock, so all-503 → Record(false) → breaker opens.
func TestLinkedInCascadeAllTiers503TripsBreaker(t *testing.T) {
	tier503 := func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		return 503, []byte(`<html><body>503 Service Unavailable</body></html>`), nil
	}
	withTiers(t, tier503, tier503)
	// Isolate breaker: FailThreshold=3 so 3 failed cascades trip it.
	withBreaker(t, breaker.New(breaker.Options{
		Name:          "test-linkedin-503",
		FailThreshold: 3,
		OpenDuration:  10 * time.Second,
	}))

	url := "https://www.linkedin.com/jobs-guest/jobs/api/seeMoreJobPostings/search?keywords=go"
	// First 3 calls: all 503 → cascade exhausted → Record(false) each.
	for i := 0; i < 3; i++ {
		_, err := linkedInRequest(context.Background(), url)
		if err == nil {
			t.Fatalf("call %d: expected cascade-exhausted error on all-503, got nil", i)
		}
		if !errors.Is(err, errLinkedInCascadeExhausted) {
			t.Fatalf("call %d: expected errLinkedInCascadeExhausted, got %v", i, err)
		}
	}

	// 4th call: breaker must be OPEN (tripped by 3 Record(false) on 503).
	// If the old bug recorded Record(true) on 503, the breaker would still be
	// closed here and this call would proceed (and fail with cascade-exhausted
	// instead of breaker.ErrOpen).
	_, err := linkedInRequest(context.Background(), url)
	if !errors.Is(err, breaker.ErrOpen) {
		t.Fatalf("expected breaker.ErrOpen on 4th call (503 storm must trip breaker), got %v", err)
	}
}

// TestLinkedInCascadeExhaustedErrorEnriched verifies Finding 3: on total
// failure the cascade returns an error that (a) still wraps the
// errLinkedInCascadeExhausted sentinel so errors.Is keeps working, and (b) is
// enriched with the LAST tier's classified kind + status so downstream
// alerting can distinguish rate-limit vs hard-block vs network-down.
func TestLinkedInCascadeExhaustedErrorEnriched(t *testing.T) {
	// Last tier (Tier-B) returns a 429 → kind=liRateLimited, status=429.
	tierA := func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		return 999, []byte(`blocked`), nil // liHardBlock
	}
	tierB := func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		return 429, []byte(`rate limited`), nil // liRateLimited — LAST tier
	}
	withTiers(t, tierA, tierB)

	_, err := linkedInRequest(context.Background(), "https://www.linkedin.com/jobs-guest/jobs/api/seeMoreJobPostings/search?keywords=go")
	if err == nil {
		t.Fatal("expected error when all tiers blocked, got nil")
	}
	// Sentinel MUST still match (wrapped with %w).
	if !errors.Is(err, errLinkedInCascadeExhausted) {
		t.Errorf("errors.Is(err, errLinkedInCascadeExhausted) = false; sentinel must be wrapped, got %v", err)
	}
	// Enrichment: last tier + status + kind in the message.
	msg := err.Error()
	if !strings.Contains(msg, "last tier=B") {
		t.Errorf("error message missing 'last tier=B': %q", msg)
	}
	if !strings.Contains(msg, "status=429") {
		t.Errorf("error message missing 'status=429' (last tier status): %q", msg)
	}
	if !strings.Contains(msg, "kind=") {
		t.Errorf("error message missing 'kind=' field: %q", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "rate_limited") {
		t.Errorf("error message kind must identify rate-limit for last tier 429, got %q", msg)
	}
}

// TestLinkedInCascadeEscalationLogs verifies Finding 3: each tier escalation
// emits a slog.Warn with {tier, status, kind, err} so operators can distinguish
// 429-throttle-everywhere from hard-block from network-down.
func TestLinkedInCascadeEscalationLogs(t *testing.T) {
	// Capture slog output by swapping the default logger.
	var buf bytes.Buffer
	orig := slog.Default()
	t.Cleanup(func() { slog.SetDefault(orig) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))

	tierA := func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		return 503, []byte(`service unavailable`), nil // liHardBlock
	}
	tierB := func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		return 0, nil, errors.New("gowowa render: connection refused") // network err
	}
	withTiers(t, tierA, tierB)

	_, _ = linkedInRequest(context.Background(), "https://www.linkedin.com/jobs-guest/jobs/api/seeMoreJobPostings/search?keywords=go")

	out := buf.String()
	// Expect 2 escalation warn lines (one per tier).
	warnCount := strings.Count(out, "level=WARN")
	if warnCount != 2 {
		t.Errorf("expected 2 WARN escalation lines, got %d in:\n%s", warnCount, out)
	}
	// Each escalation must carry tier, status, kind fields.
	for _, want := range []string{"tier=A", "tier=B", "status=", "kind="} {
		if !strings.Contains(out, want) {
			t.Errorf("slog output missing %q in:\n%s", want, out)
		}
	}
}

// Ensure engine.Cfg is safe to call (TestMain sets FetchTimeout).
var _ = engine.Cfg
