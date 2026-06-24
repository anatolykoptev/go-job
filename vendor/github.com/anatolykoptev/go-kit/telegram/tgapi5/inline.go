package tgapi5

import (
	tgbotapi "github.com/OvyFlash/telegram-bot-api"
)

// InlineResult is a kit-level representation of a single inline query result article.
// The concrete tgbotapi type is an implementation detail hidden inside BotInlineAnswerer.
type InlineResult struct {
	ID          string
	Title       string
	MessageText string
}

// InlineAnswerer is the interface for answering Telegram inline queries.
// It is declared in this package (rather than middleware) because its
// parameter type InlineResult is defined here; no kit middleware currently
// consumes inline queries.
type InlineAnswerer interface {
	AnswerInlineQuery(queryID string, results []InlineResult, cacheTime int, isPersonal bool) error
}

// BotInlineAnswerer wraps *tgbotapi.BotAPI and satisfies InlineAnswerer.
type BotInlineAnswerer struct {
	bot *tgbotapi.BotAPI
}

// NewInlineAnswerer returns a *BotInlineAnswerer backed by bot.
func NewInlineAnswerer(bot *tgbotapi.BotAPI) *BotInlineAnswerer {
	return &BotInlineAnswerer{bot: bot}
}

// AnswerInlineQuery answers a Telegram inline query with article results.
// Satisfies InlineAnswerer.
func (a *BotInlineAnswerer) AnswerInlineQuery(queryID string, results []InlineResult, cacheTime int, isPersonal bool) error {
	apiResults := make([]interface{}, len(results))
	for i, r := range results {
		article := tgbotapi.NewInlineQueryResultArticle(r.ID, r.Title, r.MessageText)
		apiResults[i] = article
	}
	cfg := tgbotapi.InlineConfig{
		InlineQueryID: queryID,
		Results:       apiResults,
		CacheTime:     cacheTime,
		IsPersonal:    isPersonal,
	}
	_, err := a.bot.Request(cfg)
	return err
}
