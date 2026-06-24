package broadcast

import (
	"context"

	"github.com/anatolykoptev/go-kit/telegram"
	tgbotapi "github.com/OvyFlash/telegram-bot-api"
)

// batchChunkSize is the maximum number of message IDs per copyMessages or
// deleteMessages API call. Telegram Bot API cap: 100 messages per call.
const batchChunkSize = 100

// ChattableSender is the interface satisfied by *tgapi5.BotSender.
// It dispatches an arbitrary tgbotapi.Chattable and returns the sent message.
type ChattableSender interface {
	SendChattable(c tgbotapi.Chattable) (tgbotapi.Message, error)
}

// BatchCopyOptions configures a batched copy broadcast.
type BatchCopyOptions struct {
	FromChatID          int64
	MessageIDs          []int   // up to 100 per API call — chunked automatically
	ToChatIDs           []int64 // destination chats
	DisableNotification bool
	ProtectContent      bool
	RemoveCaption       bool
}

// TargetResult holds the per-target outcome of a broadcast operation.
//
// ChunkErrs has one entry per chunk dispatched (index aligned); nil = success.
// Err is the first non-nil entry from ChunkErrs (convenience field).
type TargetResult struct {
	ChatID    int64
	ChunkErrs []error // per-chunk errors; nil entries = success
	Err       error   // first non-nil from ChunkErrs (convenience)
}

// AllOK reports whether every chunk succeeded (no errors at all).
func (r TargetResult) AllOK() bool {
	for _, e := range r.ChunkErrs {
		if e != nil {
			return false
		}
	}
	return true
}

// AnyOK reports whether at least one chunk succeeded.
func (r TargetResult) AnyOK() bool {
	for _, e := range r.ChunkErrs {
		if e == nil {
			return true
		}
	}
	return false
}

// Broadcaster dispatches batched copy operations over a ChattableSender with
// rate-limiting via an embedded Pacer. The Pacer gates each per-target dispatch
// so broadcast storms (429) are avoided even at large target counts.
//
// Construct with NewBroadcaster and supply a *Pacer; the Pacer's SendFn is
// not used for copy — only its interval (rate) and retry/DLQ machinery apply.
type Broadcaster struct {
	sender ChattableSender
	pacer  *Pacer
}

// NewBroadcaster returns a Broadcaster backed by sender and paced by p.
// p controls the inter-target rate. Use NewPacer with desired RPS and DLQ
// callbacks. The Pacer's SendFn is not called by Copy; supply a no-op if needed:
//
//	p := broadcast.NewPacer(func(_ context.Context, _ int64, _ string) error { return nil },
//	    broadcast.WithRPS(30))
//	b := broadcast.NewBroadcaster(sender, p)
func NewBroadcaster(sender ChattableSender, p *Pacer) *Broadcaster {
	return &Broadcaster{sender: sender, pacer: p}
}

// Copy sends opts.MessageIDs to every chat in opts.ToChatIDs using copyMessages.
// MessageIDs are automatically chunked at batchChunkSize (100) per API call.
//
// Rate limiting: one inter-target delay tick is consumed per target, matching
// the Pacer's configured RPS. Transient errors on a chunk (e.g. 429) are
// retried up to the Pacer's maxRetries with fixed delay. Terminal errors
// populate ChunkErrs without retry.
//
// Returns one TargetResult per destination chat. TargetResult.ChunkErrs is
// index-aligned to the chunks; TargetResult.Err is the first non-nil entry.
func (b *Broadcaster) Copy(ctx context.Context, opts BatchCopyOptions) []TargetResult {
	results := make([]TargetResult, 0, len(opts.ToChatIDs))
	chunks := chunkInts(opts.MessageIDs, batchChunkSize)

	for i, chatID := range opts.ToChatIDs {
		if ctx.Err() != nil {
			results = append(results, TargetResult{
				ChatID:    chatID,
				ChunkErrs: []error{ctx.Err()},
				Err:       ctx.Err(),
			})
			continue
		}

		// Rate-limit between targets (skip delay before the very first target).
		if i > 0 {
			if err := b.pacer.waitInterval(ctx); err != nil {
				results = append(results, TargetResult{
					ChatID:    chatID,
					ChunkErrs: []error{err},
					Err:       err,
				})
				continue
			}
		}

		chunkErrs := make([]error, len(chunks))
		var firstErr error

		for j, chunk := range chunks {
			if ctx.Err() != nil {
				chunkErrs[j] = ctx.Err()
				if firstErr == nil {
					firstErr = ctx.Err()
				}
				continue
			}
			cfg := tgbotapi.CopyMessagesConfig{
				BaseChat: tgbotapi.BaseChat{
					ChatConfig:          tgbotapi.ChatConfig{ChatID: chatID},
					DisableNotification: opts.DisableNotification,
					ProtectContent:      opts.ProtectContent,
				},
				FromChat:      tgbotapi.ChatConfig{ChatID: opts.FromChatID},
				MessageIDs:    chunk,
				RemoveCaption: opts.RemoveCaption,
			}
			err := b.pacer.sendChattableWithRetry(ctx, func() error {
				_, e := b.sender.SendChattable(cfg)
				return e
			})
			chunkErrs[j] = err
			if err != nil && firstErr == nil {
				firstErr = err
			}
		}

		results = append(results, TargetResult{
			ChatID:    chatID,
			ChunkErrs: chunkErrs,
			Err:       firstErr,
		})
	}
	return results
}

// chunkInts splits s into sub-slices of at most size elements.
// Returns an empty (non-nil) slice of chunks when s is empty.
func chunkInts(s []int, size int) [][]int {
	if len(s) == 0 {
		return [][]int{}
	}
	chunks := make([][]int, 0, (len(s)+size-1)/size)
	for len(s) > 0 {
		n := size
		if n > len(s) {
			n = len(s)
		}
		chunks = append(chunks, s[:n])
		s = s[n:]
	}
	return chunks
}

// sendChattableWithRetry retries fn on transient errors, matching Pacer's retry policy.
func (p *Pacer) sendChattableWithRetry(ctx context.Context, fn func() error) error {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := fn()
		if err == nil {
			return nil
		}
		if !telegram.IsTransientError(err) {
			return err
		}
		lastErr = err
		if attempt < maxRetries {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-waitTimer(retryDelay):
			}
		}
	}
	return lastErr
}
