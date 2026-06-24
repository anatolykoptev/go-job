package broadcast

import (
	"context"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
)

// Delete deletes messageIDs from chatID using batched deleteMessages calls.
// IDs are automatically chunked at batchChunkSize (100) per API call.
// Returns the first error encountered, if any; remaining chunks are still attempted.
func (b *Broadcaster) Delete(ctx context.Context, chatID int64, messageIDs []int) error {
	var firstErr error
	for _, chunk := range chunkInts(messageIDs, batchChunkSize) {
		if ctx.Err() != nil {
			if firstErr == nil {
				firstErr = ctx.Err()
			}
			break
		}
		cfg := tgbotapi.DeleteMessagesConfig{
			BaseChatMessages: tgbotapi.BaseChatMessages{
				ChatConfig: tgbotapi.ChatConfig{ChatID: chatID},
				MessageIDs: chunk,
			},
		}
		if _, err := b.sender.SendChattable(cfg); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
