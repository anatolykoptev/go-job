package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// APIError is a structured error returned by LLM API calls.
// Use errors.As to extract status code, body, and error type from callers.
type APIError struct {
	StatusCode int
	Body       string
	Type       string // parsed from JSON response if available (e.g. "rate_limit_error")
	Code       string // parsed from JSON response if available (e.g. "context_length_exceeded")
	Retryable  bool
	// RetryAfter is the server-suggested delay before retry, parsed from
	// the HTTP Retry-After response header (RFC 7231 §7.1.3). Zero when
	// the header is absent or unparseable. Callers retrying APIError
	// should honour this value when non-zero instead of their own
	// backoff schedule.
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	if e.Type != "" {
		return fmt.Sprintf("llm: HTTP %d (%s): %s", e.StatusCode, e.Type, e.Body)
	}
	return fmt.Sprintf("llm: HTTP %d: %s", e.StatusCode, e.Body)
}

func newAPIError(statusCode int, body string, retryable bool, retryAfter time.Duration) *APIError {
	e := &APIError{
		StatusCode: statusCode,
		Body:       body,
		Retryable:  retryable,
		RetryAfter: retryAfter,
	}
	// Try to extract error type/code from JSON body (OpenAI/Anthropic format).
	var parsed struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &parsed) == nil {
		e.Type = parsed.Error.Type
		e.Code = parsed.Error.Code
	}
	return e
}

func isRetryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func asRetryable(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Retryable
}

// asFailover reports whether err is a per-model "request too large" failure:
// the request exceeds THIS model's context window or per-minute token budget.
//
// Such an error is NOT retryable on the same endpoint — the identical request
// recurs and would 413/400 again — but the NEXT model in a fallback chain may
// have a larger context window or a higher token budget. So the endpoint loop
// should ADVANCE to it rather than abort the whole chain.
//
// Recognised signals (cross-provider, observed on the cliproxyapi fleet):
//   - HTTP 413 Payload Too Large — Groq emits this with type "tokens" when a
//     single request exceeds the model's per-minute token (TPM) budget.
//   - HTTP 400 with error code "context_length_exceeded" — the OpenAI-family
//     context-window-overflow shape.
//
// A plain 400 (malformed request) is deliberately NOT a failover: it recurs
// identically on every model, so the chain must abort, not burn every endpoint.
//
// Note: 413 is matched on status alone (any body) — treated as model-specific.
// A non-model 413 shared by every endpoint (e.g. a gateway payload-size limit)
// would still advance and burn the chain, surfacing the same error as before
// just after N attempts. Acceptable for the same-proxy chains this targets.
func asFailover(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.StatusCode == http.StatusRequestEntityTooLarge { // 413
		return true
	}
	if apiErr.StatusCode == http.StatusBadRequest && apiErr.Code == "context_length_exceeded" {
		return true
	}
	// An empty-completion response (HTTP 200, no usable content) is non-retryable
	// on THIS model — a deterministic reasoning model truncated by the output-token
	// budget recurs the same truncation — but the NEXT model in the chain may have
	// a different reasoning/output profile and answer. Advance, don't abort.
	if isEmptyCompletion(err) {
		return true
	}
	return false
}

// emptyCompletionCode is the APIError.Code sentinel for a 200-OK response whose
// assistant message carried no usable content (no text, no tool calls). Observed
// in production when a reasoning model exhausts its max_tokens budget on
// reasoning tokens before emitting any answer (finish_reason=length, content="").
// A non-empty body that merely fails to JSON-parse is NOT this case — that
// surfaces as decode/parse handling upstream; this sentinel is specifically the
// "model produced nothing" semantic failure.
const emptyCompletionCode = "empty_completion"

// newEmptyCompletionError builds the structured error for an empty completion.
// StatusCode is 0 (the HTTP call succeeded; the failure is semantic — 200 would look like success in logs), Retryable
// is false (re-issuing the identical request recurs the same empty output on a
// deterministic endpoint — so this is a chain-failover signal, not a
// same-endpoint retry). finishReason is carried in the body for observability.
func newEmptyCompletionError(finishReason string) *APIError {
	return &APIError{
		StatusCode: 0,
		Body:       "llm: empty completion (no content, no tool calls; finish_reason=" + finishReason + ")",
		Code:       emptyCompletionCode,
		Retryable:  false,
	}
}

// isEmptyCompletion reports whether err is the empty-completion sentinel.
func isEmptyCompletion(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code == emptyCompletionCode
}

// errTypeUnknown is the error_type label value for errors that do not match
// any known class. Centralised to avoid the string literal appearing 3+ times
// (goconst min-occurrences: 3 in .golangci.yml).
const errTypeUnknown = "unknown"

// ClassifyErrorType returns a low-cardinality error_type label value for
// Prometheus / OTel instrumentation. The mapping derives from the existing
// classifiers in this package — no new detection logic is introduced here.
//
// Values (align with the fleet failure-class taxonomy shared names):
//   - auth_expiry       — 401; or 403 without a quota marker
//   - dependency_block  — 429; quota-class 503; or 403 with a quota marker
//   - context_overflow  — 413 (TPM/payload too large) or 400 context_length_exceeded
//   - empty_completion  — 200 with no usable content (reasoning truncated by max_tokens)
//   - transient         — retryable 5xx / network (not quota-class)
//   - client            — non-auth, non-overflow 4xx (bad request, etc.)
//   - unknown           — non-APIError errors or anything unclassified
//
// Returns "" when err is nil (success path label).
func ClassifyErrorType(err error) string {
	if err == nil {
		return ""
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return errTypeUnknown
	}
	// auth_expiry: explicit auth rejection (401 always; 403 unless it carries a quota marker)
	if apiErr.StatusCode == http.StatusUnauthorized {
		return "auth_expiry"
	}
	// calls checkMarkers directly — 403 does NOT go through isQuotaError to avoid triggering cooldown
	if apiErr.StatusCode == http.StatusForbidden {
		if checkMarkers(apiErr) {
			return "dependency_block"
		}
		return "auth_expiry"
	}
	// dependency_block: provider-side quota / rate-limit denial
	// reuses isQuotaError logic (429 + quota-class 503)
	if isQuotaError(err) {
		return "dependency_block"
	}
	// empty_completion: model returned a 200 with no usable content (reasoning
	// model truncated by max_tokens before emitting any answer). Checked BEFORE
	// asFailover because asFailover also matches this class for chain-advance,
	// but the metric label must distinguish it from context_overflow.
	if isEmptyCompletion(err) {
		return "empty_completion"
	}
	// context_overflow: request too large for this model
	// reuses asFailover logic (413 + 400 context_length_exceeded)
	if asFailover(err) {
		return "context_overflow"
	}
	// transient: retryable 5xx / network errors (remaining ones — 500,502,504 etc.)
	if apiErr.Retryable {
		return "transient"
	}
	// client: 4xx that aren't auth/quota/overflow (bad request, not-found, etc.)
	if apiErr.StatusCode >= http.StatusBadRequest && apiErr.StatusCode < http.StatusInternalServerError {
		return "client"
	}
	return errTypeUnknown
}

// parseRetryAfter parses the HTTP Retry-After header per RFC 7231. The
// value can be either a non-negative integer number of seconds or an
// HTTP-date. Returns 0 on empty or unparseable input.
func parseRetryAfter(h string) time.Duration {
	if h == "" {
		return 0
	}
	// Seconds form.
	if secs, err := strconv.Atoi(h); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	// HTTP-date form.
	if t, err := http.ParseTime(h); err == nil {
		d := time.Until(t)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}
