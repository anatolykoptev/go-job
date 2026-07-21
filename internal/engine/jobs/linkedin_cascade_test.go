package jobs

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// withTiers swaps the three cascade tier functions and restores them on cleanup.
func withTiers(t *testing.T, t1, t2, t3 linkedInTierFunc) {
	t.Helper()
	orig1, orig2, orig3 := linkedInTier1Fetch, linkedInTier2Fetch, linkedInTier3Fetch
	t.Cleanup(func() {
		linkedInTier1Fetch = orig1
		linkedInTier2Fetch = orig2
		linkedInTier3Fetch = orig3
	})
	linkedInTier1Fetch = t1
	linkedInTier2Fetch = t2
	linkedInTier3Fetch = t3
}

func TestLinkedInCascadeEscalatesThroughTiers(t *testing.T) {
	var calls1, calls2, calls3 atomic.Int32

	tier1 := func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		calls1.Add(1)
		return 200, []byte(`<html><body>checkpoint</body></html>`), nil // liChallenge
	}
	tier2 := func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		calls2.Add(1)
		return 403, []byte(`forbidden`), nil // liHardBlock
	}
	tier3 := func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		calls3.Add(1)
		return 200, []byte(`<html><body>clean job results page</body></html>`), nil // liOK
	}
	withTiers(t, tier1, tier2, tier3)

	body, err := linkedInRequest(context.Background(), "https://www.linkedin.com/jobs-guest/jobs/api/seeMoreJobPostings/search?keywords=go")
	if err != nil {
		t.Fatalf("linkedInRequest returned error: %v", err)
	}
	if string(body) != `<html><body>clean job results page</body></html>` {
		t.Errorf("expected tier-3 body, got %q", string(body))
	}
	if got := calls1.Load(); got != 1 {
		t.Errorf("tier1 call count = %d, want 1", got)
	}
	if got := calls2.Load(); got != 1 {
		t.Errorf("tier2 call count = %d, want 1", got)
	}
	if got := calls3.Load(); got != 1 {
		t.Errorf("tier3 call count = %d, want 1", got)
	}
}

func TestLinkedInCascadeShortCircuitsOnTier1OK(t *testing.T) {
	var calls1, calls2, calls3 atomic.Int32

	tier1 := func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		calls1.Add(1)
		return 200, []byte(`<html><body>clean results</body></html>`), nil // liOK
	}
	tier2 := func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		calls2.Add(1)
		return 200, []byte(`should not be called`), nil
	}
	tier3 := func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		calls3.Add(1)
		return 200, []byte(`should not be called`), nil
	}
	withTiers(t, tier1, tier2, tier3)

	body, err := linkedInRequest(context.Background(), "https://www.linkedin.com/jobs-guest/jobs/api/seeMoreJobPostings/search?keywords=go")
	if err != nil {
		t.Fatalf("linkedInRequest returned error: %v", err)
	}
	if string(body) != `<html><body>clean results</body></html>` {
		t.Errorf("expected tier-1 body, got %q", string(body))
	}
	if got := calls1.Load(); got != 1 {
		t.Errorf("tier1 call count = %d, want 1", got)
	}
	if got := calls2.Load(); got != 0 {
		t.Errorf("tier2 call count = %d, want 0 (short-circuit)", got)
	}
	if got := calls3.Load(); got != 0 {
		t.Errorf("tier3 call count = %d, want 0 (short-circuit)", got)
	}
}

func TestLinkedInCascadeAllTiersBlocked(t *testing.T) {
	var calls1, calls2, calls3 atomic.Int32

	tier1 := func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		calls1.Add(1)
		return 999, []byte(`blocked`), nil // liHardBlock
	}
	tier2 := func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		calls2.Add(1)
		return 429, []byte(`rate limited`), nil // liRateLimited
	}
	tier3 := func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		calls3.Add(1)
		return 0, nil, errors.New("gowowa render: connection refused") // err
	}
	withTiers(t, tier1, tier2, tier3)

	_, err := linkedInRequest(context.Background(), "https://www.linkedin.com/jobs-guest/jobs/api/seeMoreJobPostings/search?keywords=go")
	if err == nil {
		t.Fatal("expected error when all tiers blocked, got nil")
	}
	if got := calls1.Load(); got != 1 {
		t.Errorf("tier1 call count = %d, want 1", got)
	}
	if got := calls2.Load(); got != 1 {
		t.Errorf("tier2 call count = %d, want 1", got)
	}
	if got := calls3.Load(); got != 1 {
		t.Errorf("tier3 call count = %d, want 1", got)
	}
}

func TestLinkedInCascadeTier1ErrorEscalates(t *testing.T) {
	// Tier 1 returns a network error (not a block signal) — cascade should escalate.
	var calls1, calls2, calls3 atomic.Int32

	tier1 := func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		calls1.Add(1)
		return 0, nil, errors.New("connection reset")
	}
	tier2 := func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		calls2.Add(1)
		return 200, []byte(`<html><body>clean via proxy</body></html>`), nil // liOK
	}
	tier3 := func(_ context.Context, _ string, _ map[string]string) (int, []byte, error) {
		calls3.Add(1)
		return 200, []byte(`should not be called`), nil
	}
	withTiers(t, tier1, tier2, tier3)

	body, err := linkedInRequest(context.Background(), "https://www.linkedin.com/jobs-guest/jobs/api/seeMoreJobPostings/search?keywords=go")
	if err != nil {
		t.Fatalf("linkedInRequest returned error: %v", err)
	}
	if string(body) != `<html><body>clean via proxy</body></html>` {
		t.Errorf("expected tier-2 body, got %q", string(body))
	}
	if got := calls1.Load(); got != 1 {
		t.Errorf("tier1 call count = %d, want 1", got)
	}
	if got := calls2.Load(); got != 1 {
		t.Errorf("tier2 call count = %d, want 1", got)
	}
	if got := calls3.Load(); got != 0 {
		t.Errorf("tier3 call count = %d, want 0 (tier 2 succeeded)", got)
	}
}

// Ensure engine.Cfg is safe to call (TestMain sets FetchTimeout).
var _ = engine.Cfg
