package miniapp

import (
	"context"
	"errors"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
)

// Sentinel errors for SavePrepared.
var (
	// ErrInvalidUserID is returned when userID is <= 0.
	ErrInvalidUserID = errors.New("miniapp: userID must be > 0")
	// ErrInvalidResult is returned when result is nil.
	ErrInvalidResult = errors.New("miniapp: result must not be nil")
	// ErrNoChatTypeAllowed is returned when PreparedOptions has all four chat-type
	// flags false; Telegram requires at least one allowed chat type.
	ErrNoChatTypeAllowed = errors.New("miniapp: at least one Allow*Chats flag must be true")
)

// PreparedOptions controls which chat types a Mini App user can target when
// sharing the prepared message. At least one of the four flags MUST be true.
type PreparedOptions struct {
	AllowUserChats    bool
	AllowBotChats     bool
	AllowGroupChats   bool
	AllowChannelChats bool
}

// PreparedSender is the minimal sender interface for savePreparedInlineMessage.
// Callers wrap *tgbotapi.BotAPI with an adapter (see tgapi5.BotPreparedSender)
// that adds context support and config-construction boilerplate.
//
// The result parameter must be one of the SDK's concrete InlineQueryResult*
// types (the SDK aliases tgbotapi.InlineQueryResult to `any`, so the compiler
// cannot enforce this; passing other types fails at JSON-marshal or at the
// Telegram API). The accepted concrete types are:
//
//   - InlineQueryResultArticle           (commonly used for share-flows)
//   - InlineQueryResultPhoto
//   - InlineQueryResultGIF
//   - InlineQueryResultMPEG4GIF
//   - InlineQueryResultVideo
//   - InlineQueryResultAudio
//   - InlineQueryResultVoice
//   - InlineQueryResultDocument
//   - InlineQueryResultLocation
//   - InlineQueryResultVenue
//   - InlineQueryResultContact
//   - InlineQueryResultGame
//   - InlineQueryResultCachedPhoto
//   - InlineQueryResultCachedGIF
//   - InlineQueryResultCachedMPEG4GIF
//   - InlineQueryResultCachedSticker
//   - InlineQueryResultCachedDocument
//   - InlineQueryResultCachedVideo
//   - InlineQueryResultCachedVoice
//   - InlineQueryResultCachedAudio
//
// Reference: https://core.telegram.org/bots/api#inlinequeryresult
type PreparedSender interface {
	SavePreparedInlineMessage(
		ctx context.Context,
		userID int64,
		result tgbotapi.InlineQueryResult,
		opts PreparedOptions,
	) (tgbotapi.PreparedInlineMessage, error)
}

// SavePrepared stores an inline message that a Mini App user can later
// share into any chat picker, returning a PreparedInlineMessage with a
// prepared_message_id and expiration_date.
//
// userID is the Telegram user ID from validated InitData (see Validate).
// result is one of the tgbotapi InlineQueryResult* concrete types.
// opts controls which chat types the message can be shared into; at least
// one of the four Allow*Chats flags must be true.
//
// Returns ErrInvalidUserID if userID <= 0, ErrInvalidResult if result is nil,
// or ErrNoChatTypeAllowed if all chat-type flags are false. Errors from the
// underlying sender are returned as-is.
//
// Reference: https://core.telegram.org/bots/api#savepreparedinlinemessage
func SavePrepared(
	ctx context.Context,
	s PreparedSender,
	userID int64,
	result tgbotapi.InlineQueryResult,
	opts PreparedOptions,
) (tgbotapi.PreparedInlineMessage, error) {
	if userID <= 0 {
		return tgbotapi.PreparedInlineMessage{}, ErrInvalidUserID
	}
	if result == nil {
		return tgbotapi.PreparedInlineMessage{}, ErrInvalidResult
	}
	if !opts.AllowUserChats && !opts.AllowBotChats && !opts.AllowGroupChats && !opts.AllowChannelChats {
		return tgbotapi.PreparedInlineMessage{}, ErrNoChatTypeAllowed
	}
	return s.SavePreparedInlineMessage(ctx, userID, result, opts)
}
