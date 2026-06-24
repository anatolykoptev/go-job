package miniapp

import (
	"context"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
)

// WebAppAnswerer is the minimal sender interface for answerWebAppQuery.
// Callers wrap *tgbotapi.BotAPI with an adapter that adds context support.
type WebAppAnswerer interface {
	AnswerWebAppQuery(ctx context.Context, queryID string, result tgbotapi.InlineQueryResult) (*tgbotapi.SentWebAppMessage, error)
}

// Reply sends a single InlineQueryResult back into the Mini App session
// identified by queryID. It is a thin wrapper around WebAppAnswerer that
// keeps the caller's code free of config-construction boilerplate.
//
// queryID is the query_id field from the validated InitData.QueryID.
// result must be one of the tgbotapi InlineQueryResult* concrete types.
func Reply(
	ctx context.Context,
	a WebAppAnswerer,
	queryID string,
	result tgbotapi.InlineQueryResult,
) (*tgbotapi.SentWebAppMessage, error) {
	return a.AnswerWebAppQuery(ctx, queryID, result)
}
