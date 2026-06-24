package tgapi5

import (
	"strings"

	"github.com/anatolykoptev/go-kit/metrics"
	tgbotapi "github.com/OvyFlash/telegram-bot-api"
)

// MetricDeleteTotal is the counter name incremented on every DeleteMessage call.
// Labels: result={ok, too_old, other_error}.
//
//   - ok         — message deleted successfully.
//   - too_old    — Telegram rejected the delete because the message is too old
//                  (error text contains "message can't be deleted" or "MESSAGE_TOO_OLD").
//   - other_error — any other failure.
const MetricDeleteTotal = "tgapi5.delete.total"

// BotMessageDeleter wraps *tgbotapi.BotAPI and satisfies middleware.MessageDeleter.
// It classifies delete errors into too_old vs other_error for observability.
type BotMessageDeleter struct {
	bot *tgbotapi.BotAPI
	m   *metrics.Registry
}

// NewMessageDeleter returns a *BotMessageDeleter backed by bot.
// m may be nil; metrics.Registry.Incr is a no-op on nil receivers.
func NewMessageDeleter(bot *tgbotapi.BotAPI, m *metrics.Registry) *BotMessageDeleter {
	return &BotMessageDeleter{bot: bot, m: m}
}

// DeleteMessage deletes the message identified by chatID+messageID.
// Satisfies middleware.MessageDeleter.
func (d *BotMessageDeleter) DeleteMessage(chatID int64, messageID int) error {
	_, err := d.bot.Request(tgbotapi.NewDeleteMessage(chatID, messageID))
	if err != nil {
		result := classifyDeleteError(err.Error())
		d.m.Incr(metrics.Label(MetricDeleteTotal, "result", result))
		return err
	}
	d.m.Incr(metrics.Label(MetricDeleteTotal, "result", "ok"))
	return nil
}

// classifyDeleteError maps a Telegram API error message to a bounded metric label.
// "too_old" is returned when the message is older than Telegram's delete window.
// "other_error" is returned for all other failures.
func classifyDeleteError(errMsg string) string {
	if strings.Contains(errMsg, "message can't be deleted") ||
		strings.Contains(errMsg, "MESSAGE_TOO_OLD") {
		return "too_old"
	}
	return "other_error"
}
