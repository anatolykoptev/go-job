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
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	kitmetrics "github.com/anatolykoptev/go-kit/metrics"
	kitnotify "github.com/anatolykoptev/go-kit/telegram/notify"

	"github.com/anatolykoptev/go_job/internal/hunt"
)

// ClaimChecker checks whether a bounty is already claimed.
// Implementations must be safe for concurrent use.
type ClaimChecker interface {
	IsClaimed(ctx context.Context, b hunt.Bounty) (claimed bool, err error)
}

// ProductNotifier wraps a go-kit ProductSink and implements hunt.Notifier.
// Methods dispatch fire-and-forget goroutines; fan-out and rate limiting are
// handled by the Pacer inside ProductSink (no semaphore needed here).
// OnSend, if set, is called with "sent", "failed", "stale", "no_date",
// "unscored", "skipped_claimed", "claim_check_error", or "notified" after
// each Notify or gate decision — it bridges into gojob_hunt_notify_total{outcome}
// via engine.IncrHuntNotify. "unscored" is a card-type marker emitted only for a
// degraded card that PASSES recency (so it never overlaps a terminal "stale"/"no_date" drop).
type ProductNotifier struct {
	sink         kitnotify.ProductSink
	chatIDs      []int64              // explicit recipient list; empty = use sink's defaultChatIDs
	maxAge       time.Duration        // recency gate for NotifyNewJob; 0 means use default (48h)
	claimChecker ClaimChecker         // optional; nil = never claimed (fail-open)
	OnSend       func(outcome string) // optional metric hook
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

// WithClaimChecker returns a copy of the notifier with the given ClaimChecker wired in.
// A nil checker is accepted (treated as "never claimed").
func (n *ProductNotifier) WithClaimChecker(cc ClaimChecker) *ProductNotifier {
	cp := *n
	cp.claimChecker = cc
	return &cp
}

// NotifyNewBounty sends a notification for a new bounty entry (fire-and-forget).
// Claimed bounties are silently skipped via shouldNotifyBounty.
func (n *ProductNotifier) NotifyNewBounty(b hunt.Bounty) {
	go func() {
		if !n.shouldNotifyBounty(context.Background(), b) {
			return
		}
		n.dispatch(formatBountyMsg(b))
	}()
}

// NotifyNewJob sends a notification for a new job entry (fire-and-forget).
// Recency gate: jobs with nil PostedAt are skipped (outcome "no_date"); jobs
// posted more than maxAge ago are skipped (outcome "stale"). Both outcomes bump
// OnSend but do NOT call dispatch. A degraded (FitBand=="unscored") card that
// PASSES recency additionally bumps OnSend("unscored") before dispatch — the
// emit lives here, after the recency gate, so an unscored job that is also stale
// counts only as "stale" (no double-count).
// score is nil when scoring is disabled or not yet available — in that case a
// degraded recency-only card is rendered. When FitBand=="unscored" (LLM failed),
// a degraded card is also rendered. For all other non-nil scores, a full fit-card
// is rendered.
// NOTE: recency gate applies to jobs only; bounties/freelance/security do not
// expire by posting date the same way — extend here if wanted.
func (n *ProductNotifier) NotifyNewJob(j hunt.Job, score *hunt.ScoreResult) {
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
	// Recency passed → terminal dispatch. A degraded (LLM-fail) card is marked
	// "unscored" HERE, after recency, so a stale unscored job is counted only as
	// "stale" — never "unscored"+"stale". "unscored" is a card-TYPE marker on
	// dispatched degraded cards (it overlaps the per-recipient "sent"/"failed"
	// from dispatch by design); the mutually-exclusive TERMINAL-DROP outcomes are
	// stale/no_date (here) and low_fit (the worker fit gate).
	if score != nil && score.FitBand == hunt.FitBandUnscored && n.OnSend != nil {
		n.OnSend("unscored")
	}
	n.dispatch(formatJobMsg(j, score))
}

// NotifyNewFreelance sends a notification for a new freelance entry (fire-and-forget).
func (n *ProductNotifier) NotifyNewFreelance(f hunt.Freelance) {
	n.dispatch(formatFreelanceMsg(f))
}

// NotifyNewSecurity sends a notification for a new security program entry (fire-and-forget).
// Security programs are NOT gated by the claim checker.
func (n *ProductNotifier) NotifyNewSecurity(s hunt.Security) {
	n.dispatch(formatSecurityMsg(s))
}

// shouldNotifyBounty returns true if the bounty should be dispatched.
// Fail-open: if claimChecker is nil or returns an error, notify anyway.
func (n *ProductNotifier) shouldNotifyBounty(ctx context.Context, b hunt.Bounty) bool {
	if n.claimChecker == nil {
		return true
	}
	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	claimed, err := n.claimChecker.IsClaimed(tctx, b)
	if err != nil {
		slog.Info("hunt_notify_outcome", "outcome", "claim_check_error", "title", b.Title, "error", err)
		n.recordOutcome("claim_check_error")
		return true // fail-open
	}
	if claimed {
		n.recordOutcome("skipped_claimed")
		return false
	}
	n.recordOutcome("notified")
	return true
}

// recordOutcome emits an outcome via OnSend if the hook is set.
func (n *ProductNotifier) recordOutcome(outcome string) {
	if n.OnSend != nil {
		n.OnSend(outcome)
	}
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

// formatBountyMsg renders a clean, scannable Telegram message for a bounty.
//
// Format:
//
//	💰 $1,500 · AppFox · 2026-01-15
//	Found SQL injection in auth endpoint
//	https://github.com/appfox/issues/123
//
// Zero guards: AmountCents==0 → no dollar part; empty Org → omitted;
// zero PostedAt falls back to FirstSeenAt; both zero → date omitted.
func formatBountyMsg(b hunt.Bounty) string {
	var parts []string

	// Amount with thousands separator (no $0)
	if b.AmountCents > 0 {
		dollars := b.AmountCents / 100
		parts = append(parts, "$"+formatThousands(dollars))
	}

	// Org
	if b.Org != "" {
		parts = append(parts, b.Org)
	}

	// Date: prefer PostedAt, fall back to FirstSeenAt
	var dateStr string
	if b.PostedAt != nil && !b.PostedAt.IsZero() {
		dateStr = b.PostedAt.Format("2006-01-02")
	} else if !b.FirstSeenAt.IsZero() {
		dateStr = b.FirstSeenAt.Format("2006-01-02")
	}
	if dateStr != "" {
		parts = append(parts, dateStr)
	}

	var sb strings.Builder
	sb.WriteString("💰")
	if len(parts) > 0 {
		sb.WriteString(" ")
		sb.WriteString(strings.Join(parts, " · "))
	}
	sb.WriteByte('\n')
	sb.WriteString(b.Title)
	sb.WriteByte('\n')
	sb.WriteString(b.URL)
	return sb.String()
}

// FormatJobMsgForTest exposes formatJobMsg for white-box tests in notify_test package.
// It is the only exported symbol in this file and exists solely for testing.
func FormatJobMsgForTest(j hunt.Job, score *hunt.ScoreResult) string {
	return formatJobMsg(j, score)
}

// formatJobMsg renders a Telegram message for a job notification.
//
// Full fit-card (score != nil AND score.FitBand != "unscored"):
//
//	[<fit_band> · <fit_score>] <title>
//	<company> · <location/remote> · <salary if present>
//
//	Why you: <≤2 fit_reasons>
//	Gaps: <≤2 fit_gaps>
//	Success: <BAND> — <success_reasoning>
//
//	<url>
//
// Degraded card (score == nil OR score.FitBand == hunt.FitBandUnscored):
//
//	[fresh · recency-only] <title>
//	<company> · <location/remote> · <salary if present>
//
//	<url>
//
// INVARIANT: the "Success:" line MUST NEVER interpolate a number.
// It is assembled from the enum band (STRONG/MODERATE/LONGSHOT) and the
// already-sanitised reasoning string only. No fmt.Sprintf with numeric arg.
func formatJobMsg(j hunt.Job, score *hunt.ScoreResult) string {
	isDegraded := score == nil || score.FitBand == hunt.FitBandUnscored

	var sb strings.Builder

	// --- Header line ---
	if isDegraded {
		fmt.Fprintf(&sb, "[fresh · recency-only] %s\n", j.Title)
	} else {
		fmt.Fprintf(&sb, "[%s · %d] %s\n", score.FitBand, score.FitScore, j.Title)
	}

	// --- Company · location/remote · salary ---
	{
		var meta []string
		if j.Company != "" {
			meta = append(meta, j.Company)
		}
		loc := j.Location
		if j.Remote != "" && j.Remote != "false" && j.Remote != "0" {
			if loc != "" {
				loc += " (remote)"
			} else {
				loc = "Remote"
			}
		}
		if loc != "" {
			meta = append(meta, loc)
		}
		if j.SalaryMin > 0 || j.SalaryMax > 0 {
			currency := j.SalaryCurrency
			if currency == "" {
				currency = "USD"
			}
			meta = append(meta, fmt.Sprintf("$%d–$%d %s", j.SalaryMin, j.SalaryMax, currency))
		}
		if len(meta) > 0 {
			sb.WriteString(strings.Join(meta, " · "))
			sb.WriteByte('\n')
		}
	}

	sb.WriteByte('\n')

	if !isDegraded {
		// --- Why you ---
		if len(score.FitReasons) > 0 {
			reasons := score.FitReasons
			if len(reasons) > 2 {
				reasons = reasons[:2]
			}
			fmt.Fprintf(&sb, "Why you: %s\n", strings.Join(reasons, "; "))
		}

		// --- Gaps ---
		if len(score.FitGaps) > 0 {
			gaps := score.FitGaps
			if len(gaps) > 2 {
				gaps = gaps[:2]
			}
			fmt.Fprintf(&sb, "Gaps: %s\n", strings.Join(gaps, "; "))
		}

		// --- Success (INVARIANT: no number interpolated here) ---
		// The success line is built from: enum band string + prose reasoning string.
		// No numeric value is ever formatted into this line.
		if score.SuccessBand != "" || score.SuccessReasoning != "" {
			band := score.SuccessBand
			if band == "" {
				band = "MODERATE" // enum-clamp fallback
			}
			// band is one of STRONG/MODERATE/LONGSHOT — no digits possible.
			// score.SuccessReasoning is a free-text string produced by the LLM prompt
			// that instructs "no percentages, no numbers" — but we do not re-validate
			// it here (the prompt + parse layer is the enforcement point).
			fmt.Fprintf(&sb, "Success: %s — %s\n", band, score.SuccessReasoning)
		}

		sb.WriteByte('\n')
	}

	sb.WriteString(j.URL)
	return sb.String()
}

// formatFreelanceMsg renders a Telegram message for a freelance posting.
//
// Format:
//
//	[source] Title
//	Budget: $N      (omitted when BudgetMax == 0)
//	Tags: a, b      (omitted when empty)
//	https://...
func formatFreelanceMsg(f hunt.Freelance) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[%s] %s\n", f.Source, f.Title)
	if f.BudgetMax > 0 {
		fmt.Fprintf(&sb, "Budget: $%s\n", formatThousands(int64(f.BudgetMax)))
	}
	if len(f.Tags) > 0 {
		fmt.Fprintf(&sb, "Tags: %s\n", strings.Join(f.Tags, ", "))
	}
	sb.WriteString(f.URL)
	return sb.String()
}

// formatSecurityMsg renders a clean, scannable Telegram message for a security program.
//
// Format:
//
//	🛡️ Max $1,500 · AppFox [bugcrowd] · 2026-01-15
//	Scope: 12 targets (api.appfox.com)
//	https://bugcrowd.com/appfox
//
// Zero guards: MaxBounty==0 → no amount; Targets empty → no scope line;
// date from FirstSeenAt.
func formatSecurityMsg(s hunt.Security) string {
	var parts []string

	// Amount
	if s.MaxBounty > 0 {
		parts = append(parts, "Max $"+formatThousands(int64(s.MaxBounty)))
	}

	// Name [platform]
	namePlatform := s.Name
	if s.Platform != "" {
		namePlatform += " [" + s.Platform + "]"
	}
	if namePlatform != "" {
		parts = append(parts, namePlatform)
	}

	// Date
	if !s.FirstSeenAt.IsZero() {
		parts = append(parts, s.FirstSeenAt.Format("2006-01-02"))
	}

	var sb strings.Builder
	sb.WriteString("🛡️")
	if len(parts) > 0 {
		sb.WriteString(" ")
		sb.WriteString(strings.Join(parts, " · "))
	}
	sb.WriteByte('\n')

	// Scope line
	if len(s.Targets) > 0 {
		first := extractHost(s.Targets[0])
		fmt.Fprintf(&sb, "Scope: %d targets (%s)\n", len(s.Targets), first)
	}

	sb.WriteString(s.URL)
	return sb.String()
}

// extractHost returns the hostname from a URL string, or the string as-is if
// it is not a valid URL (e.g. already a bare domain like "*.example.com").
func extractHost(target string) string {
	u, err := url.Parse(target)
	if err == nil && u.Host != "" {
		return u.Host
	}
	return target
}

// formatThousands formats an integer with comma thousands separators.
// e.g. 1500 → "1,500", 1000000 → "1,000,000".
func formatThousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	rem := len(s) % 3
	if rem > 0 {
		b.WriteString(s[:rem])
	}
	for i := rem; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}
