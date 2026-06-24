// Package broadcast provides a rate-limited broadcaster for Telegram messages.
//
// # Overview
//
// Pacer sends a message to a list of subscriber chat IDs at a controlled rate
// (default 30 sends/second). It retries transient errors (detected via
// telegram.IsTransientError) and routes terminal failures (e.g. "bot was
// blocked") to an optional dead-letter callback.
//
// # Usage
//
//	p := broadcast.NewPacer(sendFn,
//	    broadcast.WithRPS(30),
//	    broadcast.WithDeadLetter(func(chatID int64, err error) {
//	        log.Printf("DLQ: chat %d: %v", chatID, err)
//	    }),
//	)
//	sent, failed, err := p.Broadcast(ctx, subscribers, "Hello!")
package broadcast

import (
	"context"
	"time"

	"github.com/anatolykoptev/go-kit/telegram"
)

// SendFn is the function used to deliver a single message to a chat.
type SendFn func(ctx context.Context, chatID int64, msg string) error

// Option configures a Pacer.
type Option func(*Pacer)

// WithRPS sets the maximum sends per second. Default is 30 per the Telegram
// Bot API global rate limit.
func WithRPS(rps int) Option {
	return func(p *Pacer) {
		if rps > 0 {
			p.interval = time.Second / time.Duration(rps)
		}
	}
}

// WithDeadLetter registers a callback that is called for each subscriber whose
// send failed with a terminal (non-transient) error after all retries. The
// callback is called synchronously in the broadcast loop.
func WithDeadLetter(fn func(chatID int64, err error)) Option {
	return func(p *Pacer) {
		p.deadLetter = fn
	}
}

// Pacer sends messages to many subscribers at a controlled rate.
type Pacer struct {
	send       SendFn
	interval   time.Duration // minimum time between sends (1s/rps)
	deadLetter func(chatID int64, err error)
}

// defaultRPS is the Telegram Bot API global rate limit.
const defaultRPS = 30

// maxRetries is the number of times a transient error is retried before
// giving up and calling the dead-letter callback.
const maxRetries = 3

// retryDelay is the base delay between retries for transient errors.
const retryDelay = 200 * time.Millisecond

// NewPacer creates a Pacer with default options (30 RPS, no dead-letter).
func NewPacer(send SendFn, opts ...Option) *Pacer {
	p := &Pacer{
		send:     send,
		interval: time.Second / time.Duration(defaultRPS),
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Broadcast sends msg to every subscriber in subs, returning the number sent
// successfully, the number that failed terminally, and any context error that
// halted the broadcast early.
//
// Transient errors (telegram.IsTransientError) are retried up to maxRetries
// times with a fixed delay. If retries are exhausted or the error is terminal,
// the subscriber is counted as failed and the dead-letter callback is invoked
// (if configured).
//
// The broadcast stops early if ctx is cancelled; in that case err = ctx.Err().
func (p *Pacer) Broadcast(ctx context.Context, subs []int64, msg string) (sent, failed int, err error) {
	last := time.Time{} // tracks last send time for pacing

	for _, chatID := range subs {
		// Honour context cancellation between sends.
		if ctx.Err() != nil {
			return sent, failed, ctx.Err()
		}

		// Rate limiting: wait until interval has elapsed since last send.
		if !last.IsZero() {
			if elapsed := time.Since(last); elapsed < p.interval {
				select {
				case <-ctx.Done():
					return sent, failed, ctx.Err()
				case <-time.After(p.interval - elapsed):
				}
			}
		}

		sendErr := p.sendWithRetry(ctx, chatID, msg)
		last = time.Now()

		if sendErr != nil {
			failed++
			if p.deadLetter != nil {
				p.deadLetter(chatID, sendErr)
			}
		} else {
			sent++
		}
	}
	return sent, failed, nil
}

// sendWithRetry attempts to send msg to chatID, retrying on transient errors.
func (p *Pacer) sendWithRetry(ctx context.Context, chatID int64, msg string) error {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := p.send(ctx, chatID, msg)
		if err == nil {
			return nil
		}
		if !telegram.IsTransientError(err) {
			// Terminal error — do not retry.
			return err
		}
		lastErr = err
		if attempt < maxRetries {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(retryDelay):
			}
		}
	}
	return lastErr
}

// waitTimer returns a channel that fires after d. Extracted for testability.
func waitTimer(d time.Duration) <-chan time.Time {
	return time.After(d)
}

// waitInterval blocks for the Pacer's inter-send interval, respecting ctx.
// Returns ctx.Err() if the context is cancelled while waiting.
func (p *Pacer) waitInterval(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(p.interval):
		return nil
	}
}
