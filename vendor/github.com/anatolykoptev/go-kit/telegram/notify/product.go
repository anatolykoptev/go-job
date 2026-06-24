package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"

	"github.com/anatolykoptev/go-kit/env"
	"github.com/anatolykoptev/go-kit/metrics"
	"github.com/anatolykoptev/go-kit/telegram"
	"github.com/anatolykoptev/go-kit/telegram/broadcast"
	"github.com/anatolykoptev/go-kit/telegram/tgapi5"
)

// BotSender is the minimal interface productSink requires from a Telegram bot.
// *tgapi5.BotSender satisfies it; test doubles can substitute a stub.
type BotSender interface {
	// Send delivers plain text to chatID (satisfies middleware.Sender).
	Send(chatID int64, text string) error
	// SendChattable delivers an arbitrary Chattable (e.g. with parse mode / markup).
	SendChattable(c tgbotapi.Chattable) (tgbotapi.Message, error)
}

// MetricProductTotal is the counter bumped on each send attempt by ProductSink.
// Labels: result={sent,failed}.
// All series are pre-touched at construction so rate() reads 0, not "no data".
const MetricProductTotal = "notify_product_total"

// defaultProductRPS mirrors broadcast.defaultRPS (30 — the Telegram Bot API
// global rate limit). Kept local because the broadcast const is unexported.
const defaultProductRPS = 30

// Product is one per-event product notification.
type Product struct {
	// Text is the already-formatted message body. The sink calls
	// telegram.PrepareForTelegram on it to detect HTML/Markdown/plain and
	// normalize to the Telegram HTML parse mode.
	Text string
	// ChatIDs is the list of recipient chat IDs. Must have at least one entry
	// unless a default was set via NewProductSinkFromEnv.
	ChatIDs []int64
}

// ProductSink delivers per-event product notifications to a bot, with
// bounded-rate fan-out (via broadcast.Pacer), retry, dead-letter, and an
// HTML→plaintext fallback on Telegram parse-entity errors.
type ProductSink interface {
	// Notify sends p to all ChatIDs in p. If p.ChatIDs is empty and the sink
	// was built with a default chat ID, the default is used. Returns the count
	// of successful and failed sends plus any context-level error that stopped
	// the broadcast early. Individual send failures are counted and dead-lettered
	// but do not abort the broadcast.
	Notify(ctx context.Context, p Product) (sent, failed int, err error)

	// NotifyTo sends text to a single chat ID, bypassing p.ChatIDs.
	// Useful for targeted sends where the recipient is determined at call time.
	NotifyTo(ctx context.Context, chatID int64, text string) error
}

// ProductOption configures a productSink.
type ProductOption func(*productSink)

// WithRPS sets the maximum sends per second for the underlying Pacer.
// Default: 30 (Telegram Bot API global rate limit).
func WithRPS(rps int) ProductOption {
	return func(s *productSink) { s.rps = rps }
}

// WithDeadLetter registers a callback invoked for each chat ID whose send
// failed terminally (non-transient, retries exhausted).
func WithDeadLetter(fn func(chatID int64, err error)) ProductOption {
	return func(s *productSink) { s.deadLetter = fn }
}

// WithProductMetrics wires a metrics registry.
// Bumps notify_product_total{result=sent} and notify_product_total{result=failed}.
func WithProductMetrics(m *metrics.Registry) ProductOption {
	return func(s *productSink) { s.m = m }
}

// WithHTMLFallback controls whether the sink tries plain-text when a Telegram
// parse-entity error is received. Default true.
func WithHTMLFallback(enabled bool) ProductOption {
	return func(s *productSink) { s.htmlFallback = enabled }
}

// withDefaultChatIDs sets the fallback recipient list used when Product.ChatIDs
// is empty. Not exported — wired internally by NewProductSinkFromEnv.
func withDefaultChatIDs(ids []int64) ProductOption {
	return func(s *productSink) { s.defaultChatIDs = ids }
}

// NewProductSink builds a ProductSink that uses sender for delivery.
// sender is typically *tgapi5.BotSender from tgapi5.NewSender, but any
// implementation of BotSender works.
func NewProductSink(sender BotSender, opts ...ProductOption) ProductSink {
	s := &productSink{
		sender:       sender,
		rps:          defaultProductRPS,
		htmlFallback: true,
	}
	for _, o := range opts {
		o(s)
	}
	preTouchProductMetrics(s.m)
	return s
}

// NewProductSinkFromEnv builds a ProductSink from environment variables.
//
// Required env var:
//   - TELEGRAM_BOT_TOKEN — bot token
//
// Optional env var:
//   - <prefix>_NOTIFY_CHAT_ID — default chat ID used when Product.ChatIDs is
//     empty, e.g. BOUNTY_NOTIFY_CHAT_ID=428660. Parsed via telegram.ParseChatID.
//
// m may be nil.
func NewProductSinkFromEnv(prefix string, m *metrics.Registry) (ProductSink, error) {
	token, err := env.Required("TELEGRAM_BOT_TOKEN")
	if err != nil {
		return nil, fmt.Errorf("notify: NewProductSinkFromEnv: %w", err)
	}
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("notify: NewProductSinkFromEnv: create bot: %w", err)
	}
	sender := tgapi5.NewSender(bot, m)

	opts := []ProductOption{WithProductMetrics(m)}

	if raw := env.Str(prefix+"_NOTIFY_CHAT_ID", ""); raw != "" {
		id, parseErr := telegram.ParseChatID(raw)
		if parseErr != nil {
			return nil, fmt.Errorf("notify: NewProductSinkFromEnv: parse %s_NOTIFY_CHAT_ID=%q: %w", prefix, raw, parseErr)
		}
		opts = append(opts, withDefaultChatIDs([]int64{id}))
	}

	return NewProductSink(sender, opts...), nil
}

// preTouchProductMetrics bumps every result combination by 0 so all series are
// registered in Prometheus from t=0. This ensures rate() returns 0 (not "no
// data") during healthy operation.
func preTouchProductMetrics(m *metrics.Registry) {
	for _, result := range []string{"sent", "failed"} {
		m.Add(metrics.Label(MetricProductTotal, "result", result), 0)
	}
}

// productSink is the concrete ProductSink implementation.
type productSink struct {
	sender         BotSender
	rps            int
	deadLetter     func(chatID int64, err error)
	m              *metrics.Registry
	htmlFallback   bool
	defaultChatIDs []int64
}

// Notify implements ProductSink.
func (s *productSink) Notify(ctx context.Context, p Product) (sent, failed int, err error) {
	chatIDs := p.ChatIDs
	if len(chatIDs) == 0 {
		chatIDs = s.defaultChatIDs
	}
	if len(chatIDs) == 0 {
		return 0, 0, errors.New("notify: Product.ChatIDs must not be empty and no default chat ID configured")
	}

	text, _ := telegram.PrepareForTelegram(p.Text)

	sendFn := s.buildSendFn(text)

	// Wrap in a dead-letter callback that also bumps the metric.
	dlq := func(chatID int64, dlErr error) {
		s.m.Incr(metrics.Label(MetricProductTotal, "result", "failed"))
		if s.deadLetter != nil {
			s.deadLetter(chatID, dlErr)
		}
	}

	pacer := broadcast.NewPacer(sendFn,
		broadcast.WithRPS(s.rps),
		broadcast.WithDeadLetter(dlq),
	)

	sent, failed, err = pacer.Broadcast(ctx, chatIDs, text)

	// Count successes in the metric (Pacer only fires DLQ for failures).
	if sent > 0 {
		s.m.Add(metrics.Label(MetricProductTotal, "result", "sent"), int64(sent))
	}
	return sent, failed, err
}

// NotifyTo implements ProductSink.
func (s *productSink) NotifyTo(ctx context.Context, chatID int64, text string) error {
	prepared, _ := telegram.PrepareForTelegram(text)
	sendFn := s.buildSendFn(prepared)
	if err := sendFn(ctx, chatID, prepared); err != nil {
		s.m.Incr(metrics.Label(MetricProductTotal, "result", "failed"))
		return err
	}
	s.m.Incr(metrics.Label(MetricProductTotal, "result", "sent"))
	return nil
}

// isParseEntityError reports whether a Telegram API error is a parse-entity
// failure (i.e. malformed HTML/Markdown that the server refused). Only this
// class of error warrants a plaintext fallback — transient errors (429, 5xx)
// should surface to Pacer's retry logic instead.
func isParseEntityError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "can't parse entities") ||
		strings.Contains(msg, "can't parse message text") ||
		strings.Contains(msg, "bad request: entity")
}

// buildSendFn returns a broadcast.SendFn that delivers HTML with a plaintext
// fallback on Telegram parse-entity errors. Other errors (429, 5xx, network)
// are returned as-is so Pacer's retry logic can handle them.
// The msg string param from Pacer is intentionally ignored — htmlText is
// pre-prepared by Notify/NotifyTo and captured in the closure.
func (s *productSink) buildSendFn(htmlText string) broadcast.SendFn {
	return func(ctx context.Context, chatID int64, _ string) error { // _ = msg from Pacer, unused: htmlText captured
		// First attempt: HTML parse mode.
		msg := tgbotapi.NewMessage(chatID, htmlText)
		msg.ParseMode = tgbotapi.ModeHTML
		msg.LinkPreviewOptions = tgbotapi.LinkPreviewOptions{IsDisabled: true}

		_, err := s.sender.SendChattable(msg)
		if err == nil {
			return nil
		}

		// Only fall back to plaintext for parse-entity errors. Return other errors
		// (transient 429, 5xx, network) so Pacer's retry logic handles them.
		if !s.htmlFallback || !isParseEntityError(err) {
			return err
		}

		// Fallback: strip HTML tags and send as plain text.
		plain := telegram.StripHTMLTags(htmlText)
		if plain == "" {
			plain = htmlText // last resort: send the raw string
		}
		slog.Warn("notify: HTML parse-entity error, falling back to plain text",
			slog.Int64("chat_id", chatID),
			slog.Any("error", err))
		plain2 := tgbotapi.NewMessage(chatID, plain)
		plain2.LinkPreviewOptions = tgbotapi.LinkPreviewOptions{IsDisabled: true}
		_, err2 := s.sender.SendChattable(plain2)
		return err2
	}
}

// Compile-time check: *tgapi5.BotSender satisfies the BotSender interface
// declared in this package.
var _ BotSender = (*tgapi5.BotSender)(nil)
