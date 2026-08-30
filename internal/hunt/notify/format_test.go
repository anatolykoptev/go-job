package notify_test

// format_test.go: format tests for bounty and security messages.
// Verifies HTML output from formatBountyMsg and formatSecurityMsg via
// the captureSink harness (no real bot required).
//
// RED-on-revert evidence:
//   - HTML assertions (<b>, <a href=): revert formatBountyMsg/formatSecurityMsg →
//     HTML tags absent, tests fail.
//   - HTML-escaping test: revert kit.EscapeHTML usage → raw "<", "&" in output,
//     test fails.
//   - Zero-amount guards: remove AmountCents>0 / MaxBounty>0 → "$0" / "Max $0" appears.
//   - Scope line guard: remove len(Targets)>0 → "Scope:" in empty-target output.

import (
	"strings"
	"sync"
	"testing"
	"time"

	"context"
	kitnotify "github.com/anatolykoptev/go-kit/telegram/notify"

	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/anatolykoptev/go_job/internal/hunt/notify"
)

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

func newCaptureSinkNotifier(chatID int64) (*notify.ProductNotifier, *captureSink) {
	cs := &captureSink{}
	n := notify.NewFromSink(cs, chatID)
	return n, cs
}

// ---------------------------------------------------------------------------
// Format tests — bounty
// ---------------------------------------------------------------------------

// TestFormatBountyMsg_FullData: verify emoji, bold amount, org, date, <a href>, title.
// RED-on-revert: revert formatBountyMsg → <b>/$1,500/<a href=...> assertions fail.
func TestFormatBountyMsg_FullData(t *testing.T) {
	ts := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	n, cs := newCaptureSinkNotifier(testChatID)

	b := hunt.Bounty{
		Title:       "Found SQL injection in auth endpoint",
		URL:         "https://github.com/appfox/issues/123",
		Org:         "AppFox",
		AmountCents: 150000,
		PostedAt:    &ts,
	}
	n.NotifyNewBounty(b)
	drainDispatch()
	msg := cs.captured()

	t.Logf("bounty msg:\n%s", msg)

	checks := []struct {
		name string
		want string
	}{
		{"emoji", "💰"},
		{"bold amount", "<b>$1,500</b>"},
		{"org", "AppFox"},
		{"date", "2026-01-15"},
		{"anchor href", `<a href="https://github.com/appfox/issues/123">`},
		{"title in link", "Found SQL injection in auth endpoint"},
		{"closing anchor", "</a>"},
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
	n, cs := newCaptureSinkNotifier(testChatID)

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
	n, cs := newCaptureSinkNotifier(testChatID)

	b := hunt.Bounty{
		Title:       "title",
		URL:         "https://example.com/1",
		Org:         "Org",
		AmountCents: 5000,
		FirstSeenAt: firstSeen,
		// PostedAt intentionally nil
	}
	n.NotifyNewBounty(b)
	drainDispatch()
	msg := cs.captured()

	if !strings.Contains(msg, "2025-12-01") {
		t.Errorf("bounty with nil PostedAt must use FirstSeenAt '2025-12-01'; got:\n%s", msg)
	}
}

// TestFormatBountyMsg_HTMLEscaping: special chars in Title and Org are HTML-escaped.
// RED-on-revert: remove kit.EscapeHTML calls → raw "<", "&" appear unescaped.
func TestFormatBountyMsg_HTMLEscaping(t *testing.T) {
	n, cs := newCaptureSinkNotifier(testChatID)

	b := hunt.Bounty{
		Title:       "Alert <script>",
		URL:         "https://example.com/xss",
		Org:         "A&B Corp",
		AmountCents: 10000,
	}
	n.NotifyNewBounty(b)
	drainDispatch()
	msg := cs.captured()

	t.Logf("escaping msg:\n%s", msg)

	// Title must be escaped in the anchor text
	if strings.Contains(msg, "<script>") {
		t.Errorf("unescaped <script> found in bounty msg; got:\n%s", msg)
	}
	if !strings.Contains(msg, "&lt;script&gt;") {
		t.Errorf("expected &lt;script&gt; in bounty msg link text; got:\n%s", msg)
	}
	// Org must be escaped
	if strings.Contains(msg, "A&B") && !strings.Contains(msg, "A&amp;B") {
		t.Errorf("unescaped & found in org; got:\n%s", msg)
	}
	if !strings.Contains(msg, "A&amp;B") {
		t.Errorf("expected A&amp;B in bounty msg; got:\n%s", msg)
	}
	// Asterisk is NOT an HTML special char — must be literal
	b2 := hunt.Bounty{Title: "Fix *important* bug", URL: "https://example.com/2", AmountCents: 100}
	n.NotifyNewBounty(b2)
	drainDispatch()
	msg2 := cs.captured()
	if !strings.Contains(msg2, "*important*") {
		t.Errorf("asterisk must remain literal in HTML output; got:\n%s", msg2)
	}
}

// ---------------------------------------------------------------------------
// Format tests — security
// ---------------------------------------------------------------------------

// TestFormatSecurityMsg_FullData: emoji, bold max, platform bracket, date, scope, link.
// RED-on-revert: revert formatSecurityMsg → <b>/[bugcrowd]/Scope:/<a href=> assertions fail.
func TestFormatSecurityMsg_FullData(t *testing.T) {
	ts := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	n, cs := newCaptureSinkNotifier(testChatID)

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
		{"bold max amount", "Max <b>$1,500</b>"},
		{"name", "AppFox"},
		{"platform bracket", "[bugcrowd]"},
		{"date", "2026-01-15"},
		{"scope count", "Scope: 3 targets"},
		{"first target host", "api.appfox.com"},
		{"anchor href", `<a href="https://bugcrowd.com/appfox">`},
		{"closing anchor", "</a>"},
	}
	for _, tc := range checks {
		if !strings.Contains(msg, tc.want) {
			t.Errorf("security msg must contain %s (%q); got:\n%s", tc.name, tc.want, msg)
		}
	}
}

// TestFormatSecurityMsg_ZeroMaxBounty_NoAmount: MaxBounty==0 → no dollar sign in header.
// RED-on-revert: remove MaxBounty>0 guard → "Max $0" appears.
func TestFormatSecurityMsg_ZeroMaxBounty_NoAmount(t *testing.T) {
	n, cs := newCaptureSinkNotifier(testChatID)

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
	n, cs := newCaptureSinkNotifier(testChatID)

	s := hunt.Security{Name: "NoScope", Platform: "hackerone", URL: "https://hackerone.com/noscope", MaxBounty: 500}
	n.NotifyNewSecurity(s)
	drainDispatch()
	msg := cs.captured()

	if strings.Contains(msg, "Scope:") {
		t.Errorf("empty Targets must omit 'Scope:' line; got:\n%s", msg)
	}
}
