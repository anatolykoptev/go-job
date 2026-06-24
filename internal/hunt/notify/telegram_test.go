package notify_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"

	kitmetrics "github.com/anatolykoptev/go-kit/metrics"
	kitnotify "github.com/anatolykoptev/go-kit/telegram/notify"

	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/anatolykoptev/go_job/internal/hunt/notify"
)

// ---------------------------------------------------------------------------
// stubSender matches kitnotify.BotSender for use in NewProductSink
// ---------------------------------------------------------------------------

type stubSender struct {
	err       error
	callCount atomic.Int32
}

func (s *stubSender) Send(_ int64, _ string) error { return s.err }

func (s *stubSender) SendChattable(_ tgbotapi.Chattable) (tgbotapi.Message, error) {
	s.callCount.Add(1)
	return tgbotapi.Message{}, s.err
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// newNotifier builds a ProductNotifier backed by sender.
// chatID is supplied as an explicit recipient so tests don't need a
// configured HUNT_NOTIFY_CHAT_ID env var; the chatID is injected via
// NewFromSink's variadic chatIDs parameter.
func newNotifier(sender *stubSender, m *kitmetrics.Registry, chatID int64) *notify.ProductNotifier {
	sink := kitnotify.NewProductSink(sender,
		kitnotify.WithRPS(1000),
		kitnotify.WithProductMetrics(m),
	)
	return notify.NewFromSink(sink, chatID)
}

// drainDispatch waits for fire-and-forget goroutines to complete.
func drainDispatch() { time.Sleep(50 * time.Millisecond) }

const testChatID int64 = 428660

// ---------------------------------------------------------------------------
// OutcomeCreated gating: each NotifyNewX method must dispatch to the bot
// ---------------------------------------------------------------------------

// TestNotifyNewBounty_SendsOnce verifies that NotifyNewBounty triggers a
// SendChattable call via ProductSink. The chatID is injected so no env vars needed.
// Red-on-revert: remove the dispatch goroutine in ProductNotifier.NotifyNewBounty
// and callCount stays 0.
func TestNotifyNewBounty_SendsOnce(t *testing.T) {
	stub := &stubSender{}
	n := newNotifier(stub, kitmetrics.NewRegistry(), testChatID)

	n.NotifyNewBounty(hunt.Bounty{Title: "fix bug", URL: "https://algora.io/1", AmountCents: 5000})
	drainDispatch()

	if stub.callCount.Load() == 0 {
		t.Error("expected at least one SendChattable call for bounty notify, got 0")
	}
}

// TestNotifyNewJob_SendsOnce verifies NotifyNewJob dispatches to the bot.
// Red-on-revert: remove dispatch call in NotifyNewJob → callCount stays 0.
func TestNotifyNewJob_SendsOnce(t *testing.T) {
	stub := &stubSender{}
	n := newNotifier(stub, kitmetrics.NewRegistry(), testChatID)

	n.NotifyNewJob(hunt.Job{Title: "SRE", Company: "Acme", URL: "https://jobs.acme.io/1", Source: "greenhouse"})
	drainDispatch()

	if stub.callCount.Load() == 0 {
		t.Error("expected at least one SendChattable call for job notify, got 0")
	}
}

// TestNotifyNewFreelance_SendsOnce verifies NotifyNewFreelance dispatches.
func TestNotifyNewFreelance_SendsOnce(t *testing.T) {
	stub := &stubSender{}
	n := newNotifier(stub, kitmetrics.NewRegistry(), testChatID)

	n.NotifyNewFreelance(hunt.Freelance{Title: "Go API", Source: "upwork", URL: "https://upwork.com/j/1"})
	drainDispatch()

	if stub.callCount.Load() == 0 {
		t.Error("expected at least one SendChattable call for freelance notify, got 0")
	}
}

// TestNotifyNewSecurity_SendsOnce verifies NotifyNewSecurity dispatches.
func TestNotifyNewSecurity_SendsOnce(t *testing.T) {
	stub := &stubSender{}
	n := newNotifier(stub, kitmetrics.NewRegistry(), testChatID)

	n.NotifyNewSecurity(hunt.Security{Name: "Uber", Platform: "HackerOne", URL: "https://hackerone.com/uber"})
	drainDispatch()

	if stub.callCount.Load() == 0 {
		t.Error("expected at least one SendChattable call for security notify, got 0")
	}
}

// ---------------------------------------------------------------------------
// OnSend metric bridge — sent counter
// ---------------------------------------------------------------------------

// TestOnSend_SentCalledOnSuccess verifies that OnSend("sent") is called after
// a successful ProductSink delivery, bridging into gojob_hunt_notify_total.
// Red-on-revert: remove the `for range sent { n.OnSend("sent") }` loop in
// dispatch() and this test fails (sentCount stays 0).
func TestOnSend_SentCalledOnSuccess(t *testing.T) {
	stub := &stubSender{} // no error → all sends succeed
	n := newNotifier(stub, kitmetrics.NewRegistry(), testChatID)

	var sentCount atomic.Int32
	n.OnSend = func(outcome string) {
		if outcome == "sent" {
			sentCount.Add(1)
		}
	}

	n.NotifyNewBounty(hunt.Bounty{Title: "fix me", URL: "https://algora.io/2", AmountCents: 200})
	drainDispatch()

	if sentCount.Load() == 0 {
		t.Error("OnSend(\"sent\") must be called once for a successful delivery, got 0 calls")
	}
}

// TestOnSend_FailedCalledOnError verifies that OnSend("failed") is called when
// the bot returns a terminal error, bridging into gojob_hunt_notify_total{outcome=failed}.
// Red-on-revert: remove the `for range failed { n.OnSend("failed") }` loop in
// dispatch() and this test fails (failedCount stays 0).
func TestOnSend_FailedCalledOnError(t *testing.T) {
	terminalErr := errors.New("Forbidden: bot was blocked by the user")
	stub := &stubSender{err: terminalErr}
	n := newNotifier(stub, kitmetrics.NewRegistry(), testChatID)

	var failedCount atomic.Int32
	n.OnSend = func(outcome string) {
		if outcome == "failed" {
			failedCount.Add(1)
		}
	}

	n.NotifyNewBounty(hunt.Bounty{Title: "broken", URL: "https://algora.io/3"})
	drainDispatch()

	if failedCount.Load() == 0 {
		t.Error("OnSend(\"failed\") must be called once for a terminal delivery failure, got 0 calls")
	}
}

// ---------------------------------------------------------------------------
// Fan-out to multiple chats via chatIDs injection
// ---------------------------------------------------------------------------

// TestFanOutToMultipleChats verifies that Notify broadcasts to all injected
// ChatIDs, not just the first one — i.e. Pacer is wired through correctly.
// Red-on-revert: replace ProductSink.Notify with a single n.sink.NotifyTo call
// and this fails (callCount would be 1 instead of 3).
func TestFanOutToMultipleChats(t *testing.T) {
	stub := &stubSender{}
	m := kitmetrics.NewRegistry()
	sink := kitnotify.NewProductSink(stub,
		kitnotify.WithRPS(1000),
		kitnotify.WithProductMetrics(m),
	)
	// Drive fan-out directly through ProductSink to confirm Pacer delivers to all recipients.
	sent, failed, err := sink.Notify(context.Background(), kitnotify.Product{
		Text:    "multi-chat test",
		ChatIDs: []int64{1, 2, 3},
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if sent != 3 {
		t.Errorf("sent=%d, want 3", sent)
	}
	if failed != 0 {
		t.Errorf("failed=%d, want 0", failed)
	}
	if stub.callCount.Load() != 3 {
		t.Errorf("SendChattable calls=%d, want 3 (one per ChatID)", stub.callCount.Load())
	}
}

// TestFanOutViaNotifier verifies that ProductNotifier with two injected chatIDs
// dispatches to both of them — i.e. chatIDs are propagated into Product.ChatIDs.
// Red-on-revert: remove the chatIDs field from ProductNotifier.dispatch() → only
// the first recipient gets the message (depends on sink default).
func TestFanOutViaNotifier(t *testing.T) {
	stub := &stubSender{}
	sink := kitnotify.NewProductSink(stub, kitnotify.WithRPS(1000))
	n := notify.NewFromSink(sink, 111, 222) // two explicit recipients

	n.NotifyNewBounty(hunt.Bounty{Title: "multi", URL: "https://algora.io/4"})
	drainDispatch()

	if got := stub.callCount.Load(); got != 2 {
		t.Errorf("SendChattable calls=%d, want 2 (one per injected chatID)", got)
	}
}

// ---------------------------------------------------------------------------
// Compile-time interface check
// ---------------------------------------------------------------------------

var _ hunt.Notifier = (*notify.ProductNotifier)(nil)
