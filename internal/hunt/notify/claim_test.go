package notify_test

// claim_test.go: tests for ClaimChecker interface, shouldNotifyBounty gate,
// and format functions (bounty + security).
//
// RED-on-revert evidence:
//   - shouldNotifyBounty tests: remove the `if !n.shouldNotifyBounty(...)` gate
//     in NotifyNewBounty → claimed=true test fails (dispatch fires when it should not).
//   - Format tests: revert formatBountyMsg/formatSecurityMsg → emoji/thousands/scope
//     assertions fail.
//   - nil-checker test: remove fail-open nil check → would panic on nil deref.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	kitnotify "github.com/anatolykoptev/go-kit/telegram/notify"

	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/anatolykoptev/go_job/internal/hunt/notify"
)

// ---------------------------------------------------------------------------
// Fake ClaimChecker — no network
// ---------------------------------------------------------------------------

type fakeClaimChecker struct {
	claimed bool
	err     error
	calls   int
	mu      sync.Mutex
}

func (f *fakeClaimChecker) IsClaimed(_ context.Context, _ hunt.Bounty) (bool, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.claimed, f.err
}

func (f *fakeClaimChecker) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// ---------------------------------------------------------------------------
// captureSink — captures the dispatched text without a real bot
// ---------------------------------------------------------------------------

// captureSink implements kitnotify.ProductSink for format tests.
// It records the last text passed to Notify.
type captureSink struct {
	mu       sync.Mutex
	lastText string
}

func (c *captureSink) Notify(_ context.Context, p kitnotify.Product) (sent, failed int, err error) {
	c.mu.Lock()
	c.lastText = p.Text
	c.mu.Unlock()
	return 1, 0, nil
}

func (c *captureSink) NotifyTo(_ context.Context, _ int64, text string) error {
	c.mu.Lock()
	c.lastText = text
	c.mu.Unlock()
	return nil
}

func (c *captureSink) captured() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastText
}

var _ kitnotify.ProductSink = (*captureSink)(nil)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newNotifierWithChecker builds a ProductNotifier with an injected ClaimChecker.
func newNotifierWithChecker(cc notify.ClaimChecker, chatID int64) (*notify.ProductNotifier, *stubSender) {
	s := &stubSender{}
	sink := kitnotify.NewProductSink(s, kitnotify.WithRPS(1000))
	n := notify.NewFromSink(sink, chatID).WithClaimChecker(cc)
	return n, s
}

func newCaptureSinkNotifier(cc notify.ClaimChecker, chatID int64) (*notify.ProductNotifier, *captureSink) {
	cs := &captureSink{}
	n := notify.NewFromSink(cs, chatID).WithClaimChecker(cc)
	return n, cs
}

// ---------------------------------------------------------------------------
// Claim gate tests
// ---------------------------------------------------------------------------

// TestClaimGate_ClaimedBounty_SkipsDispatch: claimed=true → no Telegram call.
// RED-on-revert: remove shouldNotifyBounty gate → callCount goes from 0 to 1.
func TestClaimGate_ClaimedBounty_SkipsDispatch(t *testing.T) {
	cc := &fakeClaimChecker{claimed: true}
	n, sender := newNotifierWithChecker(cc, testChatID)

	var outcomes []string
	var mu sync.Mutex
	n.OnSend = func(o string) {
		mu.Lock()
		outcomes = append(outcomes, o)
		mu.Unlock()
	}

	n.NotifyNewBounty(hunt.Bounty{Title: "claimed", URL: "https://algora.io/1", AmountCents: 5000})
	drainDispatch()

	if sender.callCount.Load() != 0 {
		t.Errorf("claimed bounty must NOT dispatch; got %d calls", sender.callCount.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	var hasSkipped bool
	for _, o := range outcomes {
		if o == "skipped_claimed" {
			hasSkipped = true
		}
	}
	if !hasSkipped {
		t.Errorf("expected OnSend(\"skipped_claimed\"); got %v", outcomes)
	}
	if cc.CallCount() == 0 {
		t.Error("ClaimChecker.IsClaimed was never called")
	}
}

// TestClaimGate_UnclaimedBounty_Dispatches: claimed=false → dispatch fires.
// RED-on-revert: negate shouldNotifyBounty result → unclaimed bounties never dispatch.
func TestClaimGate_UnclaimedBounty_Dispatches(t *testing.T) {
	cc := &fakeClaimChecker{claimed: false}
	n, sender := newNotifierWithChecker(cc, testChatID)

	var outcomes []string
	var mu sync.Mutex
	n.OnSend = func(o string) {
		mu.Lock()
		outcomes = append(outcomes, o)
		mu.Unlock()
	}

	n.NotifyNewBounty(hunt.Bounty{Title: "open", URL: "https://algora.io/2", AmountCents: 1000})
	drainDispatch()

	if sender.callCount.Load() == 0 {
		t.Error("unclaimed bounty must dispatch; got 0 calls")
	}
	mu.Lock()
	defer mu.Unlock()
	var hasNotified bool
	for _, o := range outcomes {
		if o == "notified" {
			hasNotified = true
		}
	}
	if !hasNotified {
		t.Errorf("expected OnSend(\"notified\"); got %v", outcomes)
	}
}

// TestClaimGate_CheckerError_FailOpen: checker error → fail-open, still dispatch.
// RED-on-revert: return false on error → bounties lost on check failure.
func TestClaimGate_CheckerError_FailOpen(t *testing.T) {
	cc := &fakeClaimChecker{err: context.DeadlineExceeded}
	n, sender := newNotifierWithChecker(cc, testChatID)

	var outcomes []string
	var mu sync.Mutex
	n.OnSend = func(o string) {
		mu.Lock()
		outcomes = append(outcomes, o)
		mu.Unlock()
	}

	n.NotifyNewBounty(hunt.Bounty{Title: "error-bounty", URL: "https://algora.io/3", AmountCents: 500})
	drainDispatch()

	if sender.callCount.Load() == 0 {
		t.Error("claim check error must fail-open (dispatch); got 0 calls")
	}
	mu.Lock()
	defer mu.Unlock()
	var hasError bool
	for _, o := range outcomes {
		if o == "claim_check_error" {
			hasError = true
		}
	}
	if !hasError {
		t.Errorf("expected OnSend(\"claim_check_error\"); got %v", outcomes)
	}
}

// TestClaimGate_NilChecker_AlwaysNotifies: nil ClaimChecker → fail-open.
// RED-on-revert: dereference claimChecker without nil check → panic.
func TestClaimGate_NilChecker_AlwaysNotifies(t *testing.T) {
	sender := &stubSender{}
	sink := kitnotify.NewProductSink(sender, kitnotify.WithRPS(1000))
	n := notify.NewFromSink(sink, testChatID) // no WithClaimChecker

	n.NotifyNewBounty(hunt.Bounty{Title: "no-checker", URL: "https://algora.io/4", AmountCents: 200})
	drainDispatch()

	if sender.callCount.Load() == 0 {
		t.Error("nil claim checker must dispatch (fail-open); got 0 calls")
	}
}

// TestClaimGate_SecurityUngated: NotifyNewSecurity bypasses ClaimChecker entirely.
// RED-on-revert: add shouldNotifyBounty gate to NotifyNewSecurity → security programs suppressed.
func TestClaimGate_SecurityUngated(t *testing.T) {
	cc := &fakeClaimChecker{claimed: true} // would block bounties
	n, sender := newNotifierWithChecker(cc, testChatID)

	n.NotifyNewSecurity(hunt.Security{Name: "Meta", Platform: "bugcrowd", URL: "https://bugcrowd.com/meta"})
	drainDispatch()

	if sender.callCount.Load() == 0 {
		t.Error("NotifyNewSecurity must NOT be gated by claim checker; got 0 calls")
	}
	if cc.CallCount() != 0 {
		t.Errorf("ClaimChecker must NOT be called for security; got %d calls", cc.CallCount())
	}
}

// ---------------------------------------------------------------------------
// Format tests — bounty
// ---------------------------------------------------------------------------

func bountyWith(amountCents int64, org string, postedAt *time.Time, firstSeenAt time.Time, title, rawURL string) hunt.Bounty {
	b := hunt.Bounty{
		Title:       title,
		URL:         rawURL,
		Org:         org,
		AmountCents: amountCents,
		FirstSeenAt: firstSeenAt,
	}
	if postedAt != nil {
		b.PostedAt = postedAt
	}
	return b
}

// TestFormatBountyMsg_FullData: verify emoji, thousands-formatted amount, org, date, title, URL.
// RED-on-revert: revert formatBountyMsg → 💰/thousands/$1,500 assertions fail.
func TestFormatBountyMsg_FullData(t *testing.T) {
	ts := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	cc := &fakeClaimChecker{claimed: false}
	n, cs := newCaptureSinkNotifier(cc, testChatID)

	b := bountyWith(150000, "AppFox", &ts, time.Time{}, "Found SQL injection in auth endpoint", "https://github.com/appfox/issues/123")
	n.NotifyNewBounty(b)
	drainDispatch()
	msg := cs.captured()

	t.Logf("bounty msg:\n%s", msg)

	checks := []struct {
		name string
		want string
	}{
		{"emoji", "💰"},
		{"amount", "$1,500"},
		{"org", "AppFox"},
		{"date", "2026-01-15"},
		{"title", b.Title},
		{"url", b.URL},
	}
	for _, tc := range checks {
		if !strings.Contains(msg, tc.want) {
			t.Errorf("bounty msg must contain %s (%q); got:\n%s", tc.name, tc.want, msg)
		}
	}
}

// TestFormatBountyMsg_ZeroAmount_NoZeroDollar: AmountCents==0 → no "$0".
// RED-on-revert: remove AmountCents>0 guard → "$0" appears.
func TestFormatBountyMsg_ZeroAmount_NoZeroDollar(t *testing.T) {
	cc := &fakeClaimChecker{claimed: false}
	n, cs := newCaptureSinkNotifier(cc, testChatID)

	n.NotifyNewBounty(hunt.Bounty{Title: "no-amount", URL: "https://algora.io/zero"})
	drainDispatch()
	msg := cs.captured()

	if strings.Contains(msg, "$0") {
		t.Errorf("zero-amount bounty must not contain '$0'; got:\n%s", msg)
	}
	if !strings.Contains(msg, "💰") {
		t.Errorf("zero-amount bounty must still contain 💰; got:\n%s", msg)
	}
}

// TestFormatBountyMsg_ZeroPostedAt_UsesFirstSeenAt: nil PostedAt → falls back to FirstSeenAt.
// RED-on-revert: remove FirstSeenAt fallback → date absent when PostedAt nil.
func TestFormatBountyMsg_ZeroPostedAt_UsesFirstSeenAt(t *testing.T) {
	firstSeen := time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)
	cc := &fakeClaimChecker{claimed: false}
	n, cs := newCaptureSinkNotifier(cc, testChatID)

	b := bountyWith(5000, "Org", nil, firstSeen, "title", "https://example.com/1")
	n.NotifyNewBounty(b)
	drainDispatch()
	msg := cs.captured()

	if !strings.Contains(msg, "2025-12-01") {
		t.Errorf("bounty with nil PostedAt must use FirstSeenAt '2025-12-01'; got:\n%s", msg)
	}
}

// ---------------------------------------------------------------------------
// Format tests — security
// ---------------------------------------------------------------------------

// TestFormatSecurityMsg_FullData: 🛡️, Max $1,500, platform bracket, date, Scope: N targets (host), URL.
// RED-on-revert: revert formatSecurityMsg → emoji/Max/scope assertions fail.
func TestFormatSecurityMsg_FullData(t *testing.T) {
	ts := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	cc := &fakeClaimChecker{claimed: false}
	n, cs := newCaptureSinkNotifier(cc, testChatID)

	s := hunt.Security{
		Name:        "AppFox",
		Platform:    "bugcrowd",
		URL:         "https://bugcrowd.com/appfox",
		MaxBounty:   1500,
		Targets:     []string{"https://api.appfox.com", "https://app.appfox.com", "https://admin.appfox.com"},
		FirstSeenAt: ts,
	}
	n.NotifyNewSecurity(s)
	drainDispatch()
	msg := cs.captured()

	t.Logf("security msg:\n%s", msg)

	checks := []struct{ name, want string }{
		{"emoji", "🛡️"},
		{"max bounty", "Max $1,500"},
		{"name", "AppFox"},
		{"platform bracket", "[bugcrowd]"},
		{"date", "2026-01-15"},
		{"scope count", "Scope: 3 targets"},
		{"first target host", "api.appfox.com"},
		{"url", s.URL},
	}
	for _, tc := range checks {
		if !strings.Contains(msg, tc.want) {
			t.Errorf("security msg must contain %s (%q); got:\n%s", tc.name, tc.want, msg)
		}
	}
}

// TestFormatSecurityMsg_ZeroMaxBounty_NoAmount: MaxBounty==0 → no dollar in header.
// RED-on-revert: remove MaxBounty>0 guard → "Max $0" appears.
func TestFormatSecurityMsg_ZeroMaxBounty_NoAmount(t *testing.T) {
	cc := &fakeClaimChecker{claimed: false}
	n, cs := newCaptureSinkNotifier(cc, testChatID)

	s := hunt.Security{Name: "FreeProgram", Platform: "h1", URL: "https://h1.com/freeprog"}
	n.NotifyNewSecurity(s)
	drainDispatch()
	msg := cs.captured()

	if strings.Contains(msg, "$") {
		t.Errorf("zero MaxBounty must produce no dollar sign in header; got:\n%s", msg)
	}
}

// TestFormatSecurityMsg_EmptyTargets_NoScopeLine: empty Targets → no "Scope:" line.
// RED-on-revert: remove len(Targets)>0 guard → "Scope: 0 targets ()" appears.
func TestFormatSecurityMsg_EmptyTargets_NoScopeLine(t *testing.T) {
	cc := &fakeClaimChecker{claimed: false}
	n, cs := newCaptureSinkNotifier(cc, testChatID)

	s := hunt.Security{Name: "NoScope", Platform: "hackerone", URL: "https://hackerone.com/noscope", MaxBounty: 500}
	n.NotifyNewSecurity(s)
	drainDispatch()
	msg := cs.captured()

	if strings.Contains(msg, "Scope:") {
		t.Errorf("empty Targets must omit 'Scope:' line; got:\n%s", msg)
	}
}
