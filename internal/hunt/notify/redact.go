// Package notify provides ingest-side Telegram notifications for hunt entries.
//
// redact.go implements PF-6: Telegram bot token redaction at the transport and
// logging layers so a leaked token never appears in error messages or log output.
//
// RedactingTransport wraps an http.RoundTripper and scrubs the bot token from
// *url.Error.URL on RoundTrip failure (Telegram API errors surface the full
// request URL — which embeds the token — in the error string).
//
// RedactingSlogHandler wraps a slog.Handler and replaces the token in all
// string/[]byte attribute values via ReplaceAttr.
package notify

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
)

const redactedPlaceholder = "[REDACTED]"

// redactString returns s with every occurrence of token replaced by
// "[REDACTED]". When token is empty, s is returned unchanged.
func redactString(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, redactedPlaceholder)
}

// redactBytes returns b with every occurrence of token replaced by
// "[REDACTED]". When token is empty, b is returned unchanged.
func redactBytes(b []byte, token string) []byte {
	if token == "" {
		return b
	}
	return bytes.ReplaceAll(b, []byte(token), []byte(redactedPlaceholder))
}

// RedactingTransport is an http.RoundTripper that wraps a base transport and
// scrubs the Telegram bot token from *url.Error failures. The Telegram Bot API
// embeds the token in the request URL (https://api.telegram.org/bot<token>/...),
// so a network or HTTP error surfaces as a *url.Error whose .URL field contains
// the raw token. RedactingTransport rewrites that field to "[REDACTED]" so the
// error string never leaks the token.
type RedactingTransport struct {
	base  http.RoundTripper
	token string
}

// NewRedactingTransport wraps base with token redaction. When base is nil,
// http.DefaultTransport is used.
func NewRedactingTransport(base http.RoundTripper, token string) *RedactingTransport {
	if base == nil {
		base = http.DefaultTransport
	}
	return &RedactingTransport{base: base, token: token}
}

// RoundTrip delegates to the base transport. On error, if the error is a
// *url.Error, the token (if present) is replaced in err.URL with "[REDACTED]"
// and the modified error is returned.
func (t *RedactingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err == nil {
		return resp, nil
	}
	if t.token == "" {
		return resp, err
	}
	var uErr *url.Error
	if errors.As(err, &uErr) {
		redacted := redactString(uErr.URL, t.token)
		if redacted != uErr.URL {
			// Return a shallow copy with the redacted URL so we never mutate
			// the original error (it may be shared/cached by the HTTP client).
			copyErr := *uErr
			copyErr.URL = redacted
			return resp, &copyErr
		}
	}
	return resp, err
}

// newRedactingClient returns an *http.Client whose transport redacts token from
// *url.Error failures. It wraps http.DefaultTransport.
func newRedactingClient(token string) *http.Client {
	return &http.Client{
		Transport: NewRedactingTransport(http.DefaultTransport, token),
	}
}

// RedactingSlogHandler is a slog.Handler wrapper that replaces the bot token
// in all string and []byte attribute values (and the message) with
// "[REDACTED]". It delegates to a wrapped base handler (by default a text
// handler writing to os.Stderr) and uses ReplaceAttr to perform the scrubbing.
type RedactingSlogHandler struct {
	inner slog.Handler
	token string
}

// NewRedactingSlogHandler wraps base with token redaction. When base is nil, a
// new slog.TextHandler writing to os.Stderr is used as the base.
func NewRedactingSlogHandler(base slog.Handler, token string) *RedactingSlogHandler {
	if base == nil {
		base = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: slog.LevelInfo,
			ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
				return redactAttr(a, token)
			},
		})
	} else {
		// Wrap the base handler so its own ReplaceAttr (if any) still runs,
		// then layer our redaction on top via a passthrough handler.
		base = &redactPassthroughHandler{inner: base, token: token}
	}
	return &RedactingSlogHandler{inner: base, token: token}
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
	redacted := slog.NewRecord(r.Time, r.Level, redactString(r.Message, h.token), r.PC)
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

// redactAttr scrubs the token from a single slog.Attr's value when the value is
// a string or []byte. Other value kinds are returned unchanged.
func redactAttr(a slog.Attr, token string) slog.Attr {
	if token == "" {
		return a
	}
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, redactString(v.String(), token))
	case slog.KindAny:
		if b, ok := v.Any().([]byte); ok {
			return slog.Any(a.Key, redactBytes(b, token))
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

// Compile-time check: *RedactingTransport satisfies http.RoundTripper.
var _ http.RoundTripper = (*RedactingTransport)(nil)
