package jobs

import (
	"errors"
	"fmt"
	"io"
)

// isBodyTruncated returns true when err is or wraps ErrBodyTruncated.
// Used by ATS fetchers to distinguish DoS-ceiling hit from genuine parse failures.
func isBodyTruncated(err error) bool {
	return errors.Is(err, ErrBodyTruncated)
}

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

// ErrBodyTruncated is returned when a response body hit the read-cap DoS
// ceiling mid-read: the reader consumed the cap and there were more bytes
// available, meaning the source exceeds the ceiling. Makes the failure visible
// as a truncation (e.g. reason=truncated in gojob_ats_fetch_errors_total, or
// the security-source "body truncated" log) rather than as a confusing
// downstream JSON parse error.
//
// Produced both by the ATS countingReader path (atsBoardDecodeWithCap) and by
// readLimitedBody (used by the security bounty readers).
var ErrBodyTruncated = errors.New("source: body truncated at read cap")

// readLimitedBody reads at most limit+1 bytes from r. If more than limit bytes
// are available (i.e. the (limit+1)th byte was read), the body exceeds the cap
// and ErrBodyTruncated is returned — instead of silently returning a truncated
// body that would later fail json.Unmarshal with a misleading "unexpected end
// of JSON input". Bodies that fit within limit are returned in full.
//
// This is the read-side guard for sources whose dataset can grow past a
// hardcoded cap (e.g. hackerone_data.json measured 17.8 MB on 2026-07-30,
// past the old 10 MB securityBodyLimit). Without it, io.LimitReader stops at
// the cap without error and io.ReadAll reports success, attributing the
// failure to the parser instead of the reader.
func readLimitedBody(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("source: body exceeds %d-byte read cap: %w", limit, ErrBodyTruncated)
	}
	return b, nil
}
