package tgapi5

import (
	"context"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
)

// Reactor wraps *tgbotapi.BotAPI and provides reaction management methods.
//
// Bot-scope (setMessageReaction):
//   - SetReaction — set one or more reactions as the bot user.
//   - ClearBotReaction — remove the bot's own reaction (empty slice semantics).
//
// Admin-scope (requires admin privileges):
//   - AdminRemoveUserReaction — remove a specific user's reaction via deleteMessageReaction.
//   - AdminRemoveAllUserReactions — remove all of a user's recent reactions via deleteAllMessageReactions.
type Reactor struct {
	bot *tgbotapi.BotAPI
}

// NewReactor returns a *Reactor backed by bot.
func NewReactor(bot *tgbotapi.BotAPI) *Reactor {
	return &Reactor{bot: bot}
}

// SetReaction sets the reactions on a message. emojis are converted to
// []ReactionType with Type="emoji". Pass isBig=true to display a big reaction.
// An empty emojis slice clears all reactions from the bot user.
func (r *Reactor) SetReaction(ctx context.Context, chatID int64, messageID int, emojis []string, isBig bool) error {
	reactions := make([]tgbotapi.ReactionType, len(emojis))
	for i, e := range emojis {
		reactions[i] = tgbotapi.ReactionType{
			Type:  tgbotapi.ReactionTypeEmoji,
			Emoji: e,
		}
	}
	cfg := tgbotapi.SetMessageReactionConfig{
		BaseChatMessage: tgbotapi.BaseChatMessage{
			ChatConfig: tgbotapi.ChatConfig{ChatID: chatID},
			MessageID:  messageID,
		},
		Reaction: reactions,
		IsBig:    isBig,
	}
	_, err := r.bot.RequestWithContext(ctx, cfg)
	return err
}

// ClearBotReaction removes the bot's own reaction from a message by sending an
// empty reaction set. This calls setMessageReaction with reaction=[] which clears
// only the bot's reaction — it does NOT wipe other users' reactions.
// For admin-scope removal of a specific user's reactions, use AdminRemoveUserReaction
// or AdminRemoveAllUserReactions.
func (r *Reactor) ClearBotReaction(ctx context.Context, chatID int64, messageID int) error {
	return r.SetReaction(ctx, chatID, messageID, nil, false)
}

// AdminRemoveUserReaction removes a specific user's reaction from a message using
// the deleteMessageReaction endpoint. Requires admin privileges in the chat.
// Either userID or actorChatID must be non-zero to identify the reaction author.
func (r *Reactor) AdminRemoveUserReaction(ctx context.Context, chatID int64, messageID int, userID int64) error {
	cfg := tgbotapi.DeleteMessageReactionConfig{
		BaseChatMessage: tgbotapi.BaseChatMessage{
			ChatConfig: tgbotapi.ChatConfig{ChatID: chatID},
			MessageID:  messageID,
		},
		UserID: userID,
	}
	_, err := r.bot.RequestWithContext(ctx, cfg)
	return err
}

// AdminRemoveAllUserReactions removes all recent reactions of a user from a chat
// using the deleteAllMessageReactions endpoint. Requires admin privileges.
// Unlike AdminRemoveUserReaction this is not scoped to a single message.
func (r *Reactor) AdminRemoveAllUserReactions(ctx context.Context, chatID int64, userID int64) error {
	cfg := tgbotapi.DeleteAllMessageReactionsConfig{
		ChatConfig: tgbotapi.ChatConfig{ChatID: chatID},
		UserID:     userID,
	}
	_, err := r.bot.RequestWithContext(ctx, cfg)
	return err
}
