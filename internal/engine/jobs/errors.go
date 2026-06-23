package jobs

import "errors"

// Sentinel errors for per-source failure classification.
//
// These errors are part of the jobs package public contract: callers (including
// engine.PlatformOutcome) use errors.Is to map them to outcome label values.
//
// ADR-J3: introduced in P3 to enrich the platform_results_total outcome
// vocabulary from {results,empty,error} to {ok,empty,error,timeout,no_key,parse_fail}.
// Wrapping existing bare errors with these sentinels is CLASSIFICATION only;
// the underlying sources are NOT fixed in this PR (that is P4).

// ErrNoAPIKey is returned when a source requires an API key that is absent in
// the current configuration. Currently wraps the indeed.go "no API key
// configured" error. Maps to outcome=no_key in the metric classifier.
var ErrNoAPIKey = errors.New("source: API key not configured")

// ErrParse is returned when a source's response body fails to unmarshal into its
// expected schema. Currently wraps habr.go's "habr career parse" error class.
// Maps to outcome=parse_fail in the metric classifier.
var ErrParse = errors.New("source: response parse failure")

// ErrBodyTruncated is returned when the ATS board response body was silently
// truncated by io.LimitReader: len(body) == atsBoardMaxBytes means the cap was
// hit and the body is almost certainly incomplete. Feeding truncated JSON to
// json.Unmarshal would produce an "unterminated string" parse error and silently
// return 0 results — returning this sentinel instead makes the failure visible
// as reason=truncated in gojob_ats_fetch_errors_total.
var ErrBodyTruncated = errors.New("source: ATS board body truncated at read cap")
