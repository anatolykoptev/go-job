// Package tgapi5 provides default implementations of the Telegram adapter
// interfaces defined in the go-kit/telegram/middleware and related packages,
// backed by github.com/OvyFlash/telegram-bot-api.
//
// # Usage
//
// Each constructor accepts a *tgbotapi.BotAPI and optional configuration.
// The returned value satisfies the corresponding interface declared in the
// consuming package (middleware, fsm, kb):
//
//	import "github.com/anatolykoptev/go-kit/telegram/tgapi5"
//
//	sender    := tgapi5.NewSender(bot, metrics)
//	answerer  := tgapi5.NewCallbackAnswerer(bot)
//	deleter   := tgapi5.NewMessageDeleter(bot, metrics)
//	inline    := tgapi5.NewInlineAnswerer(bot)
//
// # Transport coupling
//
// This package imports tgbotapi/v5 directly. Consumers that need a
// different Telegram SDK implement the target interfaces themselves;
// go-kit stays SDK-agnostic at the interface layer.
//
// # Metrics
//
// Two metric names are exported as package-level constants so callers
// can reference them in dashboards and tests without string duplication:
//
//   - MetricSendTotal   — labelled by result (ok/error)
//   - MetricDeleteTotal — labelled by result (ok/too_old/other_error)
package tgapi5
