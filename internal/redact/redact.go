// Package redact scrubs credentials that ride in a request URL out of error
// values and strings before they reach a log line or a returned error.
//
// Why it exists: Telegram's Bot API (https://api.telegram.org/bot<token>/...)
// and some Google APIs (?key=...) carry the credential in the URL. net/http
// builds a *url.Error — whose Error() prints the full URL — only AFTER
// Transport.RoundTrip returns, inside http.Client.Do. The only place that sees
// that error is the code that called Client.Do, so redaction has to wrap the
// client (HTTPClient), not the transport.
package redact

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Placeholder replaces a literal secret.
const Placeholder = "[REDACTED]"

// botSegmentPlaceholder replaces the whole bot<id>:<secret> path segment.
const botSegmentPlaceholder = "bot<redacted>"

// botSegmentRE matches the credential segment of a Telegram Bot API URL
// ("bot123456789:AA..." in /bot<token>/method and /file/bot<token>/path).
// It works without knowing the token, so a token that rotated, or one the
// process never saw in its env, is still scrubbed.
var botSegmentRE = regexp.MustCompile(`bot\d{6,}:[A-Za-z0-9_-]{20,}`)

// String returns s with the Telegram bot segment and every non-empty secret
// (raw and URL-query-escaped form) replaced.
func String(s string, secrets ...string) string {
	s = botSegmentRE.ReplaceAllString(s, botSegmentPlaceholder)
	for _, sec := range secrets {
		if sec == "" {
			continue
		}
		s = strings.ReplaceAll(s, sec, Placeholder)
		if esc := url.QueryEscape(sec); esc != sec {
			s = strings.ReplaceAll(s, esc, Placeholder)
		}
	}
	return s
}

// Error returns err with the same scrubbing applied. A *url.Error keeps its
// type (and so Timeout()/Temporary()/Unwrap); only its URL and, if needed, the
// wrapped Err are rewritten. Any other error whose message holds a secret is
// replaced by a plain message-only error — the chain is cut on purpose, so
// errors.As cannot hand the original URL back to a caller.
// The input is never mutated: errors may be shared.
func Error(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	if ue, ok := err.(*url.Error); ok { //nolint:errorlint // top level only: a wrapped one falls through to the message path, which keeps the outer text
		c := *ue
		c.URL = String(ue.URL, secrets...)
		c.Err = Error(ue.Err, secrets...)
		return &c
	}
	msg := err.Error()
	if clean := String(msg, secrets...); clean != msg {
		return &scrubbedError{msg: clean}
	}
	return err
}

type scrubbedError struct{ msg string }

func (e *scrubbedError) Error() string { return e.msg }

// HTTPClient wraps an *http.Client so every error from Do is scrubbed. It
// satisfies the HTTPClient interfaces of telegram-bot-api and friends.
type HTTPClient struct {
	inner   *http.Client
	secrets []string
}

// NewHTTPClient wraps c. A nil c means &http.Client{}. The Telegram bot
// segment is always scrubbed; secrets adds literal values (a query-string
// API key, say).
func NewHTTPClient(c *http.Client, secrets ...string) *HTTPClient {
	if c == nil {
		c = &http.Client{}
	}
	return &HTTPClient{inner: c, secrets: secrets}
}

// Do performs the request and scrubs any returned error.
func (h *HTTPClient) Do(req *http.Request) (*http.Response, error) {
	resp, err := h.inner.Do(req)
	if err != nil {
		return resp, Error(err, h.secrets...)
	}
	return resp, nil
}
