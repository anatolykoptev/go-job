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
//   - HUNT_NOTIFY_MAX_AGE — max posting age for job notifications (default 48h); postings older than this are silently skipped; nil PostedAt is also skipped
package notify

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	kitmetrics "github.com/anatolykoptev/go-kit/metrics"
	kitnotify "github.com/anatolykoptev/go-kit/telegram/notify"

	"github.com/anatolykoptev/go_job/internal/hunt"
)

// ProductNotifier wraps a go-kit ProductSink and implements hunt.Notifier.
// Methods dispatch fire-and-forget goroutines; fan-out and rate limiting are
// handled by the Pacer inside ProductSink (no semaphore needed here).
// OnSend, if set, is called with "sent", "failed", "stale", or "no_date" after
// each Notify or recency-gate decision — it bridges into
// gojob_hunt_notify_total{outcome} via engine.IncrHuntNotify.
type ProductNotifier struct {
	sink    kitnotify.ProductSink
	chatIDs []int64              // explicit recipient list; empty = use sink's defaultChatIDs
	maxAge  time.Duration        // recency gate for NotifyNewJob; 0 means use default (48h)
	OnSend  func(outcome string) // optional metric hook
}

// defaultMaxAge is the recency gate applied when HUNT_NOTIFY_MAX_AGE is not set.
const defaultMaxAge = 48 * time.Hour

// NewFromEnv constructs a ProductNotifier using go-kit's NewProductSinkFromEnv.
//
// Required env:
//   - TELEGRAM_BOT_TOKEN   — bot token (read by go-kit)
//   - HUNT_NOTIFY_CHAT_ID  — default recipient chat ID (prefix="HUNT")
//
// Optional env:
//   - HUNT_NOTIFY_MAX_AGE  — max posting age for job notifications (default 48h);
//     postings older than this are silently skipped; nil PostedAt is also skipped.
//
// m may be nil (go-kit/metrics.Registry is nil-safe).
// Returns an error if TELEGRAM_BOT_TOKEN is missing or the BotAPI handshake fails.
func NewFromEnv(m *kitmetrics.Registry) (*ProductNotifier, error) {
	sink, err := kitnotify.NewProductSinkFromEnv("HUNT", m)
	if err != nil {
		return nil, fmt.Errorf("hunt notify: %w", err)
	}
	maxAge := defaultMaxAge
	if raw, ok := os.LookupEnv("HUNT_NOTIFY_MAX_AGE"); ok {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("hunt notify: invalid HUNT_NOTIFY_MAX_AGE %q: %w", raw, err)
		}
		maxAge = d
	}
	return &ProductNotifier{sink: sink, maxAge: maxAge}, nil
}

// NewFromSink constructs a ProductNotifier from a pre-built ProductSink.
// Used in tests and wherever a fully-configured sink is injected.
// chatIDs, when non-empty, overrides the sink's default recipients — useful
// in tests where the sink was built without a default chat ID.
// The recency gate defaults to 48h (defaultMaxAge).
func NewFromSink(sink kitnotify.ProductSink, chatIDs ...int64) *ProductNotifier {
	return &ProductNotifier{sink: sink, chatIDs: chatIDs, maxAge: defaultMaxAge}
}

// NewFromSinkWithMaxAge constructs a ProductNotifier from a pre-built ProductSink
// with an explicit maxAge for the NotifyNewJob recency gate.
// Used in tests that need a specific maxAge without relying on the env var.
// chatIDs, when non-empty, overrides the sink's default recipients.
func NewFromSinkWithMaxAge(sink kitnotify.ProductSink, maxAge time.Duration, chatIDs ...int64) *ProductNotifier {
	return &ProductNotifier{sink: sink, chatIDs: chatIDs, maxAge: maxAge}
}

// NotifyNewBounty sends a notification for a new bounty entry (fire-and-forget).
func (n *ProductNotifier) NotifyNewBounty(b hunt.Bounty) {
	n.dispatch(formatBountyMsg(b))
}

// NotifyNewJob sends a notification for a new job entry (fire-and-forget).
// Recency gate: jobs with nil PostedAt are skipped (outcome "no_date"); jobs
// posted more than maxAge ago are skipped (outcome "stale"). Both outcomes bump
// OnSend but do NOT call dispatch.
// NOTE: recency gate applies to jobs only; bounties/freelance/security do not
// expire by posting date the same way — extend here if wanted.
func (n *ProductNotifier) NotifyNewJob(j hunt.Job) {
	if j.PostedAt == nil {
		if n.OnSend != nil {
			n.OnSend("no_date")
		}
		return
	}
	if time.Since(*j.PostedAt) > n.maxAge {
		if n.OnSend != nil {
			n.OnSend("stale")
		}
		return
	}
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
