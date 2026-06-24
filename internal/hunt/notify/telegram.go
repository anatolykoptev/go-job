// Package notify provides ingest-side Telegram notifications for hunt entries.
// Notifications fire on OutcomeCreated — any ingest path (search-tool, MCP, cron).
// This replaces the 3 hand-rolled monitor goroutines (bounty_monitor.go,
// freelance_monitor.go, security_monitor.go) that only covered the monitor path.
//
// Transport: go-kit ProductSink (own bot, rate-limited fan-out via broadcast.Pacer).
// VAELOR_NOTIFY_URL is no longer used and can be removed from compose/apps.yml.
//
// Env vars required at deploy:
//   - TELEGRAM_BOT_TOKEN  — bot token for the HUNT_NOTIFY bot
//   - HUNT_NOTIFY_CHAT_ID — default recipient chat ID (e.g. 428660)
package notify

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	kitmetrics "github.com/anatolykoptev/go-kit/metrics"
	kitnotify "github.com/anatolykoptev/go-kit/telegram/notify"

	"github.com/anatolykoptev/go_job/internal/hunt"
)

// ProductNotifier wraps a go-kit ProductSink and implements hunt.Notifier.
// Methods dispatch fire-and-forget goroutines; fan-out and rate limiting are
// handled by the Pacer inside ProductSink (no semaphore needed here).
// OnSend, if set, is called with "sent" or "failed" after each Notify call —
// it bridges into gojob_hunt_notify_total{outcome} via engine.IncrHuntNotify.
type ProductNotifier struct {
	sink     kitnotify.ProductSink
	chatIDs  []int64         // explicit recipient list; empty = use sink's defaultChatIDs
	OnSend   func(outcome string) // optional metric hook
}

// NewFromEnv constructs a ProductNotifier using go-kit's NewProductSinkFromEnv.
//
// Required env:
//   - TELEGRAM_BOT_TOKEN   — bot token (read by go-kit)
//   - HUNT_NOTIFY_CHAT_ID  — default recipient chat ID (prefix="HUNT")
//
// m may be nil (go-kit/metrics.Registry is nil-safe).
// Returns an error if TELEGRAM_BOT_TOKEN is missing or the BotAPI handshake fails.
func NewFromEnv(m *kitmetrics.Registry) (*ProductNotifier, error) {
	sink, err := kitnotify.NewProductSinkFromEnv("HUNT", m)
	if err != nil {
		return nil, fmt.Errorf("hunt notify: %w", err)
	}
	return &ProductNotifier{sink: sink}, nil
}

// NewFromSink constructs a ProductNotifier from a pre-built ProductSink.
// Used in tests and wherever a fully-configured sink is injected.
// chatIDs, when non-empty, overrides the sink's default recipients — useful
// in tests where the sink was built without a default chat ID.
func NewFromSink(sink kitnotify.ProductSink, chatIDs ...int64) *ProductNotifier {
	return &ProductNotifier{sink: sink, chatIDs: chatIDs}
}

// NotifyNewBounty sends a notification for a new bounty entry (fire-and-forget).
func (n *ProductNotifier) NotifyNewBounty(b hunt.Bounty) {
	n.dispatch(formatBountyMsg(b))
}

// NotifyNewJob sends a notification for a new job entry (fire-and-forget).
func (n *ProductNotifier) NotifyNewJob(j hunt.Job) {
	n.dispatch(formatJobMsg(j))
}

// NotifyNewFreelance sends a notification for a new freelance entry (fire-and-forget).
func (n *ProductNotifier) NotifyNewFreelance(f hunt.Freelance) {
	n.dispatch(formatFreelanceMsg(f))
}

// NotifyNewSecurity sends a notification for a new security program entry (fire-and-forget).
func (n *ProductNotifier) NotifyNewSecurity(s hunt.Security) {
	n.dispatch(formatSecurityMsg(s))
}

// dispatch sends msg asynchronously via ProductSink.Notify.
// Successful and failed send counts are forwarded to OnSend if set.
// n.chatIDs, when non-empty, are passed as Product.ChatIDs; otherwise the
// sink's configured defaultChatIDs are used (set via HUNT_NOTIFY_CHAT_ID).
func (n *ProductNotifier) dispatch(msg string) {
	go func() {
		sent, failed, err := n.sink.Notify(context.Background(), kitnotify.Product{Text: msg, ChatIDs: n.chatIDs})
		if err != nil {
			slog.Warn("hunt notify: send error", slog.Any("error", err))
		}
		if n.OnSend != nil {
			for range sent {
				n.OnSend("sent")
			}
			for range failed {
				n.OnSend("failed")
			}
		}
	}()
}

// Compile-time check: *ProductNotifier satisfies hunt.Notifier.
var _ hunt.Notifier = (*ProductNotifier)(nil)

func formatBountyMsg(b hunt.Bounty) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "New Bounty")
	if b.AmountCents > 0 {
		fmt.Fprintf(&sb, " $%d", b.AmountCents/100)
	}
	fmt.Fprintf(&sb, "\n%s\n", b.Title)
	if len(b.Skills) > 0 {
		fmt.Fprintf(&sb, "Skills: %s\n", strings.Join(b.Skills, ", "))
	}
	sb.WriteString(b.URL)
	return sb.String()
}

func formatJobMsg(j hunt.Job) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[%s] %s\n", j.Source, j.Title)
	if j.Company != "" {
		fmt.Fprintf(&sb, "Company: %s\n", j.Company)
	}
	if j.SalaryMin > 0 || j.SalaryMax > 0 {
		fmt.Fprintf(&sb, "Salary: $%d–$%d\n", j.SalaryMin, j.SalaryMax)
	}
	sb.WriteString(j.URL)
	return sb.String()
}

func formatFreelanceMsg(f hunt.Freelance) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[%s] %s\n", f.Source, f.Title)
	if f.BudgetMax > 0 {
		fmt.Fprintf(&sb, "Budget: $%d\n", f.BudgetMax)
	}
	if len(f.Tags) > 0 {
		fmt.Fprintf(&sb, "Tags: %s\n", strings.Join(f.Tags, ", "))
	}
	sb.WriteString(f.URL)
	return sb.String()
}

func formatSecurityMsg(s hunt.Security) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "New Security Program [%s]\n%s\n", s.Platform, s.Name)
	if s.MaxBounty > 0 {
		fmt.Fprintf(&sb, "Max bounty: $%d\n", s.MaxBounty)
	}
	if len(s.Targets) > 0 {
		limit := 3
		if len(s.Targets) < limit {
			limit = len(s.Targets)
		}
		fmt.Fprintf(&sb, "Scope: %s\n", strings.Join(s.Targets[:limit], ", "))
	}
	sb.WriteString(s.URL)
	return sb.String()
}
