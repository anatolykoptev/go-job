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
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
	"github.com/anatolykoptev/go-kit/env"
	kitmetrics "github.com/anatolykoptev/go-kit/metrics"
	kit "github.com/anatolykoptev/go-kit/telegram"
	kitnotify "github.com/anatolykoptev/go-kit/telegram/notify"
	"github.com/anatolykoptev/go-kit/telegram/tgapi5"

	"github.com/anatolykoptev/go_job/internal/hunt"
)

// ProductNotifier wraps a go-kit ProductSink and implements hunt.Notifier.
// Methods dispatch fire-and-forget goroutines; fan-out and rate limiting are
// handled by the Pacer inside ProductSink (no semaphore needed here).
// OnSend, if set, is called with "sent", "failed", "stale", "no_date", or
// "unscored" after each Notify or gate decision — it bridges into
// gojob_hunt_notify_total{outcome} via engine.IncrHuntNotify.
// "unscored" is a card-type marker emitted only for a degraded card that
// PASSES recency (so it never overlaps a terminal "stale"/"no_date" drop).
type ProductNotifier struct {
	sink    kitnotify.ProductSink
	chatIDs []int64              // explicit recipient list; empty = use sink's defaultChatIDs
	maxAge  atomic.Int64         // recency gate for NotifyNewJob (nanoseconds); 0 means use default (48h)
	token   string               // bot token for health checks (empty when constructed via NewFromSink)
	OnSend  func(outcome string) // optional metric hook
}

// defaultMaxAge is the recency gate applied when HUNT_NOTIFY_MAX_AGE is not set.
const defaultMaxAge = 48 * time.Hour

// MaxAgeUpdater is the optional interface a Notifier may implement to allow
// the hunt worker to update the recency gate at runtime (from DB settings).
// ProductNotifier implements this; test fakes may too.
type MaxAgeUpdater interface {
	SetMaxAge(d time.Duration)
}

// SetMaxAge updates the recency gate threshold atomically. Called by the hunt
// worker per-cycle so admin-UI changes to notify_max_age apply without restart.
// A zero or negative value resets to the default (48h).
func (n *ProductNotifier) SetMaxAge(d time.Duration) {
	if d <= 0 {
		d = defaultMaxAge
	}
	n.maxAge.Store(int64(d))
}

// maxAgeOrZero returns the current recency gate as a time.Duration.
// Returns 0 if unset (caller should apply defaultMaxAge).
func (n *ProductNotifier) maxAgeOrZero() time.Duration {
	v := n.maxAge.Load()
	if v == 0 {
		return 0
	}
	return time.Duration(v)
}

// NewFromEnv constructs a ProductNotifier with a redacting HTTP client so the
// bot token is never leaked in *url.Error messages (PF-6).
//
// Instead of calling kitnotify.NewProductSinkFromEnv (which creates its own bot
// via tgbotapi.NewBotAPI with a plain http.Client), we build the bot locally
// with a RedactingTransport-wrapped client and then hand the bot to
// kitnotify.NewProductSink.
//
// SECURITY NOTE (#182): The Telegram Bot API embeds the token in the URL path
// (https://api.telegram.org/bot<token>/...). The RedactingTransport scrubs the
// token from *url.Error and slog output, but the token IS still visible in:
//   - outbound proxy access logs (if a proxy intercepts HTTPS)
//   - network monitoring/PCAP (if TLS is terminated by a middlebox)
//
// This is an upstream limitation of the OvyFlash/telegram-bot-api library which
// uses URL-path auth, not a Bearer header. Migrating to a header-based client
// would require forking the vendor library. The risk is accepted because:
//  1. go-job connects directly to api.telegram.org (no proxy in the path)
//  2. The redacting transport covers the go-job-side leak vectors (logs/errors)
//  3. The token has limited scope (send messages to specific chat IDs only)
//
// Required env:
//   - TELEGRAM_BOT_TOKEN    — bot token (env.Required; fatal if missing)
//   - HUNT_NOTIFY_CHAT_ID   — default recipient chat ID (parsed via
//     telegram.ParseChatID; optional but recommended)
//
// Optional env:
//   - HUNT_NOTIFY_MAX_AGE   — max posting age for job notifications (default 48h);
//     postings older than this are silently skipped; nil PostedAt is also skipped.
//
// m may be nil (go-kit/metrics.Registry is nil-safe).
// Returns an error if TELEGRAM_BOT_TOKEN is missing or the BotAPI handshake fails.
func NewFromEnv(m *kitmetrics.Registry) (*ProductNotifier, error) {
	token, err := env.Required("TELEGRAM_BOT_TOKEN")
	if err != nil {
		return nil, fmt.Errorf("hunt notify: %w", err)
	}

	// Build the bot with a redacting HTTP client so *url.Error failures never
	// expose the token in their URL field.
	redactingClient := newRedactingClient(token)
	bot, err := tgbotapi.NewBotAPIWithClient(token, tgbotapi.APIEndpoint, redactingClient)
	if err != nil {
		return nil, fmt.Errorf("hunt notify: create bot: %w", err)
	}

	sender := tgapi5.NewSender(bot, m)

	opts := []kitnotify.ProductOption{kitnotify.WithProductMetrics(m)}
	sink := kitnotify.NewProductSink(sender, opts...)

	// Read HUNT_NOTIFY_CHAT_ID and inject it as the notifier's explicit
	// recipient list. go-kit's withDefaultChatIDs is unexported, so we set
	// chatIDs on the ProductNotifier instead — dispatch passes them as
	// Product.ChatIDs which takes precedence over the sink's default.
	var chatIDs []int64
	if raw := env.Str("HUNT_NOTIFY_CHAT_ID", ""); raw != "" {
		id, parseErr := kit.ParseChatID(raw)
		if parseErr != nil {
			return nil, fmt.Errorf("hunt notify: parse HUNT_NOTIFY_CHAT_ID=%q: %w", raw, parseErr)
		}
		chatIDs = []int64{id}
	}

	maxAge := env.MustDuration("HUNT_NOTIFY_MAX_AGE", defaultMaxAge)
	n := &ProductNotifier{sink: sink, chatIDs: chatIDs, token: token}
	n.maxAge.Store(int64(maxAge))
	return n, nil
}

// NewFromSink constructs a ProductNotifier from a pre-built ProductSink.
// Used in tests and wherever a fully-configured sink is injected.
// chatIDs, when non-empty, overrides the sink's default recipients — useful
// in tests where the sink was built without a default chat ID.
// The recency gate defaults to 48h (defaultMaxAge).
func NewFromSink(sink kitnotify.ProductSink, chatIDs ...int64) *ProductNotifier {
	n := &ProductNotifier{sink: sink, chatIDs: chatIDs}
	n.maxAge.Store(int64(defaultMaxAge))
	return n
}

// NewFromSinkWithMaxAge constructs a ProductNotifier from a pre-built ProductSink
// with an explicit maxAge for the NotifyNewJob recency gate.
// Used in tests that need a specific maxAge without relying on the env var.
// chatIDs, when non-empty, overrides the sink's default recipients.
func NewFromSinkWithMaxAge(sink kitnotify.ProductSink, maxAge time.Duration, chatIDs ...int64) *ProductNotifier {
	n := &ProductNotifier{sink: sink, chatIDs: chatIDs}
	n.maxAge.Store(int64(maxAge))
	return n
}

// NotifyNewBounty sends a notification for a new bounty entry (fire-and-forget).
func (n *ProductNotifier) NotifyNewBounty(b hunt.Bounty) {
	n.dispatch(formatBountyMsg(b))
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
	maxAge := n.maxAgeOrZero()
	if maxAge == 0 {
		maxAge = defaultMaxAge
	}
	if time.Since(*j.PostedAt) > maxAge {
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

// HealthCheck validates the Telegram bot token by calling GetMe.
// Returns nil if the token is valid, an error otherwise.
// Returns nil (no-op) when the notifier was constructed without a token
// (e.g. via NewFromSink in tests).
func (n *ProductNotifier) HealthCheck(ctx context.Context) error {
	if n.token == "" {
		return nil // no token to validate (test constructor)
	}
	bot, err := tgbotapi.NewBotAPIWithClient(n.token, tgbotapi.APIEndpoint, newRedactingClient(n.token))
	if err != nil {
		return fmt.Errorf("hunt notify: health check create bot: %w", err)
	}
	if _, err := bot.GetMeWithContext(ctx); err != nil {
		return fmt.Errorf("hunt notify: health check GetMe: %w", err)
	}
	return nil
}

// Compile-time check: *ProductNotifier satisfies hunt.Notifier.
var _ hunt.Notifier = (*ProductNotifier)(nil)

// formatBountyMsg renders an HTML Telegram message for a bounty.
//
// Format:
//
//	💰 <b>$1,500</b> · AppFox · 2026-01-15
//	<a href="https://github.com/appfox/issues/123">Found SQL injection in auth endpoint</a>
//
// Zero guards: AmountCents==0 → no dollar part; empty Org → omitted;
// zero PostedAt falls back to FirstSeenAt; both zero → date omitted.
// HTML special chars in Org and Title are escaped via kit.EscapeHTML.
// PrepareForTelegram in the sink detects HTML tags and routes through
// SanitizeHTML → RepairHTMLNesting automatically.
func formatBountyMsg(b hunt.Bounty) string {
	var parts []string

	// Amount with thousands separator (no $0)
	if b.AmountCents > 0 {
		dollars := b.AmountCents / 100
		amountStr := "$" + formatThousands(dollars)
		if b.Currency != "" && b.Currency != "USD" {
			amountStr += " " + b.Currency
		}
		parts = append(parts, "<b>"+amountStr+"</b>")
	}

	// Org (HTML-escaped)
	if b.Org != "" {
		parts = append(parts, kit.EscapeHTML(b.Org))
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

	// Clickable link: <a href="URL">Title</a>
	escapedURL := kit.EscapeHTML(b.URL)
	escapedTitle := kit.EscapeHTML(b.Title)
	sb.WriteString(`<a href="`)
	sb.WriteString(escapedURL)
	sb.WriteString(`">`)
	sb.WriteString(escapedTitle)
	sb.WriteString(`</a>`)

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

// formatSecurityMsg renders an HTML Telegram message for a security program.
//
// Format:
//
//	🛡️ Max <b>$1,500</b> · AppFox [bugcrowd] · 2026-01-15
//	Scope: 12 targets (api.appfox.com)
//	<a href="https://bugcrowd.com/appfox">AppFox [bugcrowd]</a>
//
// Zero guards: MaxBounty==0 → no amount; Targets empty → no scope line;
// date from FirstSeenAt.
// HTML special chars in Name and Platform are escaped via kit.EscapeHTML.
func formatSecurityMsg(s hunt.Security) string {
	var parts []string

	// Amount
	if s.MaxBounty > 0 {
		parts = append(parts, "Max <b>$"+formatThousands(int64(s.MaxBounty))+"</b>")
	}

	// Name [platform] — both HTML-escaped
	escapedName := kit.EscapeHTML(s.Name)
	escapedPlatform := kit.EscapeHTML(s.Platform)
	namePlatform := escapedName
	if escapedPlatform != "" {
		namePlatform += " [" + escapedPlatform + "]"
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
		first := kit.EscapeHTML(kit.Truncate(extractHost(s.Targets[0]), 40))
		fmt.Fprintf(&sb, "Scope: %d targets (%s)\n", len(s.Targets), first)
	}

	// Clickable link: <a href="URL">Name [platform]</a>
	escapedURL := kit.EscapeHTML(s.URL)
	sb.WriteString(`<a href="`)
	sb.WriteString(escapedURL)
	sb.WriteString(`">`)
	sb.WriteString(namePlatform)
	sb.WriteString(`</a>`)

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
