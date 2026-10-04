// Package redact scrubs credentials that ride in a request URL out of error
// values and strings before they reach a log line or a returned error.
//
// The implementation lives in go-kit's telegram/tgsafe (the Telegram bot
// segment bot<id>:<secret>, including its query-escaped forms, plus literal
// secrets such as a ?key= API key). This package keeps the call sites short;
// wrap the HTTP client with tgsafe.NewHTTPClient, not the transport: net/http
// builds the *url.Error, whose Error() prints the full URL, only after
// RoundTrip returns, inside http.Client.Do.
package redact

import "github.com/anatolykoptev/go-kit/telegram/tgsafe"

// Placeholder replaces a literal secret.
const Placeholder = tgsafe.Placeholder

// String returns s with the Telegram bot segment and every non-empty secret
// (raw and URL-escaped forms) replaced.
func String(s string, secrets ...string) string { return tgsafe.Scrub(s, secrets...) }

// Error returns err with the same scrubbing applied. A *url.Error keeps its
// type (Timeout()/Unwrap); any other error whose message holds a secret is
// replaced by a message-only error. The input is never mutated.
func Error(err error, secrets ...string) error { return tgsafe.Error(err, secrets...) }
