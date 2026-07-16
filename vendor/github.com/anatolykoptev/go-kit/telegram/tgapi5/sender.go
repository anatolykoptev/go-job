package tgapi5

import (
	tgbotapi "github.com/OvyFlash/telegram-bot-api"
	"github.com/anatolykoptev/go-kit/metrics"
)

// MetricSendTotal is the counter name incremented on every Send or SendChattable call.
// Labels: result={ok, error}.
const MetricSendTotal = "tgapi5.send.total"

// BotSender wraps *tgbotapi.BotAPI and satisfies middleware.Sender.
// It also exposes SendChattable for callers that need parse mode, markup, or other options.
type BotSender struct {
	bot *tgbotapi.BotAPI
	m   *metrics.Registry
}

// NewSender returns a *BotSender backed by bot.
// m may be nil; metrics.Registry.Incr is a no-op on nil receivers.
func NewSender(bot *tgbotapi.BotAPI, m *metrics.Registry) *BotSender {
	return &BotSender{bot: bot, m: m}
}

// Send sends a plain-text message to chatID.
// Satisfies middleware.Sender.
func (s *BotSender) Send(chatID int64, text string) error {
	cfg := tgbotapi.NewMessage(chatID, text)
	if _, err := s.bot.Send(cfg); err != nil {
		s.m.Incr(metrics.Label(MetricSendTotal, "result", "error"))
		return err
	}
	s.m.Incr(metrics.Label(MetricSendTotal, "result", "ok"))
	return nil
}

// SendChattable sends an arbitrary Chattable config (e.g. with markup or parse mode).
// Useful when plain Send is insufficient.
func (s *BotSender) SendChattable(c tgbotapi.Chattable) (tgbotapi.Message, error) {
	msg, err := s.bot.Send(c)
	if err != nil {
		s.m.Incr(metrics.Label(MetricSendTotal, "result", "error"))
		return tgbotapi.Message{}, err
	}
	s.m.Incr(metrics.Label(MetricSendTotal, "result", "ok"))
	return msg, nil
}
