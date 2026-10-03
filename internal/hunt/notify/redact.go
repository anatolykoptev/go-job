// Package notify provides ingest-side Telegram notifications for hunt entries.
//
// redact.go keeps the Telegram bot token out of log output. The Bot API embeds
// the token in the request URL, so a failed call surfaces as a *url.Error whose
// message carries it. Two layers cover that, each at the point that actually
// sees the error:
//
//   - the http.Client handed to the bot is wrapped (redact.HTTPClient, see
//     newTelegramBot) so the error returned by Client.Do is already scrubbed;
//   - RedactingSlogHandler scrubs every attribute that reaches the logger,
//     including slog.Any("error", err), which arrives as KindAny holding an
//     error rather than as a string.
package notify

import (
	"context"
	"io"
	"log/slog"
	"os"

	"github.com/anatolykoptev/go_job/internal/redact"
)

// redactBytes returns b with the token (and any Telegram bot segment) scrubbed.
func redactBytes(b []byte, token string) []byte {
	return []byte(redact.String(string(b), token))
}

// RedactingSlogHandler is a slog.Handler wrapper that scrubs the bot token from
// the message and from every attribute: strings, []byte, errors and groups.
type RedactingSlogHandler struct {
	inner slog.Handler
	token string
}

// NewRedactingSlogHandler wraps base with token redaction. When base is nil, a
// new slog.TextHandler writing to os.Stderr is used as the base.
func NewRedactingSlogHandler(base slog.Handler, token string) *RedactingSlogHandler {
	if base == nil {
		return newRedactingStreamHandler(os.Stderr, token)
	}
	// Wrap the base handler so its own ReplaceAttr (if any) still runs,
	// then layer our redaction on top via a passthrough handler.
	return &RedactingSlogHandler{inner: &redactPassthroughHandler{inner: base, token: token}, token: token}
}

// newRedactingStreamHandler is the nil-base construction: a standalone
// TextHandler on w with the redaction in ReplaceAttr. w is a parameter so a
// test can run the exact handler main.go installs.
func newRedactingStreamHandler(w io.Writer, token string) *RedactingSlogHandler {
	inner := slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: slog.LevelInfo,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			return redactAttr(a, token)
		},
	})
	return &RedactingSlogHandler{inner: inner, token: token}
}

// redactPassthroughHandler wraps an existing handler and redacts the token from
// every attribute value and the message before delegating to the inner handler.
type redactPassthroughHandler struct {
	inner slog.Handler
	token string
}

func (h *redactPassthroughHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *redactPassthroughHandler) Handle(ctx context.Context, r slog.Record) error {
	redacted := slog.NewRecord(r.Time, r.Level, redact.String(r.Message, h.token), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		redacted.AddAttrs(redactAttr(a, h.token))
		return true
	})
	return h.inner.Handle(ctx, redacted)
}

func (h *redactPassthroughHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		redacted[i] = redactAttr(a, h.token)
	}
	return &redactPassthroughHandler{inner: h.inner.WithAttrs(redacted), token: h.token}
}

func (h *redactPassthroughHandler) WithGroup(name string) slog.Handler {
	return &redactPassthroughHandler{inner: h.inner.WithGroup(name), token: h.token}
}

// redactAttr scrubs one attribute. Strings and []byte are scrubbed in place;
// an error (the slog.Any("error", err) shape) is rendered through Error() and
// emitted as a string, which is what every handler prints for it anyway;
// groups are walked. The Telegram bot segment is scrubbed even when token is
// empty.
func redactAttr(a slog.Attr, token string) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, redact.String(v.String(), token))
	case slog.KindGroup:
		attrs := v.Group()
		out := make([]any, len(attrs))
		for i, ga := range attrs {
			out[i] = redactAttr(ga, token)
		}
		return slog.Group(a.Key, out...)
	case slog.KindAny:
		switch x := v.Any().(type) {
		case error:
			return slog.String(a.Key, redact.String(x.Error(), token))
		case []byte:
			return slog.Any(a.Key, redactBytes(x, token))
		}
	}
	return a
}

// Enabled delegates to the wrapped handler.
func (h *RedactingSlogHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

// Handle delegates to the wrapped handler. The inner handler already has
// redaction wired via ReplaceAttr (when constructed with a nil base) or via the
// redactPassthroughHandler (when wrapping an existing handler).
func (h *RedactingSlogHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.inner.Handle(ctx, r)
}

// WithAttrs delegates to the wrapped handler.
func (h *RedactingSlogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h.inner.WithAttrs(attrs)
}

// WithGroup delegates to the wrapped handler.
func (h *RedactingSlogHandler) WithGroup(name string) slog.Handler {
	return h.inner.WithGroup(name)
}

// Compile-time check: *RedactingSlogHandler satisfies slog.Handler.
var _ slog.Handler = (*RedactingSlogHandler)(nil)
