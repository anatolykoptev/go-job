package fetch

import (
	"bytes"
	"net/http"
	"strings"
)

// blockSig classifies the blocking signal from an HTTP response.
type blockSig int

const (
	sigNone blockSig = iota
	sigHard // 403/429/503, CF challenge, PerimeterX/DataDome/Imperva markers
	sigSoft // 200 OK + body < 512 bytes + text/html (suspect minimal block page)
	sigTLS  // connection-level error (often anti-bot TCP reset)
)

// softBlockBodyThreshold is the maximum body size (bytes) for a 200 OK response
// to be considered a soft block page. Real HTML pages are typically >1KB.
const softBlockBodyThreshold = 512

// classifyBlock inspects a response and returns the appropriate block signal.
// Called after a direct (no-proxy) fetch to decide whether to escalate to proxy.
//
// Body marker checks are performed on a lowercased copy to handle mixed-case
// anti-bot HTML (e.g. "DataDome", "_pxAppId", "Just A Moment").
func classifyBlock(status int, hdrs http.Header, body []byte, err error) blockSig {
	if err != nil {
		return sigTLS
	}

	switch status {
	case http.StatusUnauthorized,      // 401
		http.StatusForbidden,          // 403
		http.StatusTooManyRequests,    // 429
		http.StatusServiceUnavailable: // 503
		return sigHard
	}

	// Cloudflare: explicit mitigation header
	if hdrs.Get("cf-mitigated") != "" {
		return sigHard
	}

	// Body checks: lowercase once, reuse for all marker comparisons.
	bodyLower := bytes.ToLower(body)

	// Cloudflare: challenge body markers
	if hdrs.Get("server") == "cloudflare" && bytes.Contains(bodyLower, []byte("__cf_chl")) {
		return sigHard
	}
	if bytes.Contains(bodyLower, []byte("just a moment")) {
		return sigHard
	}

	// PerimeterX
	if bytes.Contains(bodyLower, []byte("px-captcha")) {
		return sigHard
	}
	if bytes.Contains(bodyLower, []byte("_pxappid")) {
		return sigHard
	}

	// DataDome
	if bytes.Contains(bodyLower, []byte("datadome")) {
		return sigHard
	}

	// Imperva / Incapsula
	if bytes.Contains(bodyLower, []byte("_incapsula_resource")) {
		return sigHard
	}

	// Akamai
	if bytes.Contains(bodyLower, []byte("akamai-bot-manager")) {
		return sigHard
	}

	// Soft block: 200 OK + tiny HTML body (real pages are typically larger)
	ct := hdrs.Get("content-type")
	if status == http.StatusOK &&
		len(body) < softBlockBodyThreshold &&
		strings.Contains(ct, "text/html") {
		return sigSoft
	}

	return sigNone
}
