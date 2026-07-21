package jobs

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// linkedInBlockKind classifies a LinkedIn response into a block category.
type linkedInBlockKind int

const (
	liOK            linkedInBlockKind = iota // 200 clean — no block markers
	liRateLimited                            // 429 — transient, retry after backoff
	liHardBlock                              // 403/999/401 — account/IP banned
	liChallenge                              // 302 authwall/checkpoint or 200 challenge body
	liNetworkError                           // transport error (no HTTP response) — cascade-level only
)

// String renders the kind for logs/alerting (distinguishes rate-limit vs
// hard-block vs network-down in cascade observability).
func (k linkedInBlockKind) String() string {
	switch k {
	case liOK:
		return "ok"
	case liRateLimited:
		return "rate_limited"
	case liHardBlock:
		return "hard_block"
	case liChallenge:
		return "challenge"
	case liNetworkError:
		return "network_error"
	default:
		return "unknown"
	}
}

// linkedInBlockMarkers are case-insensitive body substrings that indicate a
// challenge / auth wall on a 200 response.
var linkedInBlockMarkers = []string{
	"authwall",
	"checkpoint",
	"challenge",
	"captcha",
	"security verification",
}

// classifyLinkedInResponse classifies an HTTP response from LinkedIn into a
// block kind. Status is classified first; for 200 responses the body is
// scanned for challenge markers.
//
// Rules:
//   - 302 → liChallenge (LinkedIn 302 from API always redirects to authwall/checkpoint).
//   - 401/403 → liHardBlock.
//   - 429 → liRateLimited.
//   - 999 → liHardBlock.
//   - 200 + body contains any block marker (case-insensitive) → liChallenge.
//   - 200 otherwise → liOK.
//   - status >= 400 (unhandled 4xx/5xx like 404/500/502/503) → liHardBlock.
//   - status >= 300 (non-302 3xx redirect — LinkedIn redirects to authwall/checkpoint) → liChallenge.
//   - otherwise (2xx like 204) → liOK.
//
// The default-by-range classification is critical: linkedInTier1Stealth returns
// (status, body, nil) for ANY status, so a 503/500 error page MUST classify as
// a hard block — otherwise the cascade short-circuits on the error page,
// records a breaker success on a failure, and returns the error-page body to
// MCP tools as a false success (issue #291).
func classifyLinkedInResponse(status int, body []byte) linkedInBlockKind {
	switch status {
	case 200:
		lb := strings.ToLower(string(body))
		for _, marker := range linkedInBlockMarkers {
			if strings.Contains(lb, marker) {
				return liChallenge
			}
		}
		return liOK
	case 302:
		// LinkedIn 302 from the API always redirects to authwall/checkpoint.
		// When a body is available, verify; otherwise default to challenge.
		if len(body) > 0 {
			lb := strings.ToLower(string(body))
			if strings.Contains(lb, "authwall") || strings.Contains(lb, "checkpoint") {
				return liChallenge
			}
		}
		return liChallenge
	case 401, 403:
		return liHardBlock
	case 429:
		return liRateLimited
	case 999:
		return liHardBlock
	default:
		// Unhandled statuses classified by range so error pages don't masquerade
		// as success. See issue #291.
		switch {
		case status >= 400:
			return liHardBlock
		case status >= 300:
			return liChallenge
		default:
			return liOK
		}
	}
}

// linkedInErrorStatusRe extracts a 3-digit HTTP status from go-linkedin error
// strings ("voyager ...: status NNN" or "voyager auth failed: status NNN").
var linkedInErrorStatusRe = regexp.MustCompile(`status (\d{3})`)

// classifyLinkedInError maps a go-linkedin Voyager error to a block kind.
// go-linkedin wraps non-200 responses as "voyager ...: status N" and 401/403
// as "voyager auth failed: status N"; a 200-with-HTML body (challenge wall) is
// reported as "voyager auth failed: HTML response".
func classifyLinkedInError(err error) linkedInBlockKind {
	if err == nil {
		return liOK
	}
	s := err.Error()
	// 200 + HTML body = challenge wall (session expired or IP blocked).
	if strings.Contains(s, "HTML response") {
		return classifyLinkedInResponse(200, []byte("challenge"))
	}
	if m := linkedInErrorStatusRe.FindStringSubmatch(s); m != nil {
		status, _ := strconv.Atoi(m[1])
		return classifyLinkedInResponse(status, nil)
	}
	return liOK
}

// errLinkedInCascadeExhausted is returned when all cascade tiers return non-OK.
var errLinkedInCascadeExhausted = errors.New("linkedin cascade exhausted: all tiers blocked")
