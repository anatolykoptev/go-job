package websearch

import (
	"bytes"
	"net/http"
	"regexp"
	"strings"

	stealth "github.com/anatolykoptev/go-stealth"
)

var reHTMLTag = regexp.MustCompile(`<[^>]*>`)

// Common Accept / Content-Type header values reused across search providers.
const (
	acceptJSON           = "application/json"
	acceptHTML           = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
	acceptFormURLEncoded = "application/x-www-form-urlencoded"
)

// CleanHTML strips HTML tags and trims whitespace.
func CleanHTML(s string) string {
	return strings.TrimSpace(reHTMLTag.ReplaceAllString(s, ""))
}

// resolveUserAgent returns the User-Agent that matches the TLS fingerprint
// the fleet's stealth client presents. When d is a *stealth.BrowserClient
// (the production concrete type behind every websearch.BrowserDoer),
// Identity().UserAgent is the exact UA paired with the installed TLS profile
// — the matched pair go-stealth guarantees by construction. When d is nil or
// a test mock (no *stealth.BrowserClient concrete type), the UA is resolved
// from the default TLS profile (ProfileChrome131 — the profile every
// stealth.NewClient() in this repo installs, since no callsite uses
// WithProfile) via UserAgentForProfile, so the UA matches the fleet's
// canonical identity regardless.
//
// This replaces the former chromeUserAgents hardcoded pool, which rotated
// through Chrome 131/130, Safari 17.2, and Firefox 115 UAs over a Chrome 131
// JA3 — a self-inconsistent pair (no real Firefox 115 produces a Chrome 131
// handshake) that was a stronger bot signal than being merely out of date.
func resolveUserAgent(d any) string {
	if bc, ok := d.(*stealth.BrowserClient); ok && bc != nil {
		return bc.Identity().UserAgent
	}
	return stealth.UserAgentForProfile(stealth.ProfileChrome131)
}

// ChromeHeadersFor returns browser-like HTTP headers for direct scraping,
// with the User-Agent derived from d's identity so it agrees with the JA3
// fingerprint the presenting stealth client actually sends. d is the
// BrowserDoer the caller will issue the request through; pass nil to resolve
// from the default profile (ProfileChrome131).
func ChromeHeadersFor(d any) map[string]string {
	return map[string]string{
		"accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
		"accept-language": "en-US,en;q=0.9",
		"accept-encoding": "gzip, deflate, br",
		"user-agent":      resolveUserAgent(d),
	}
}

// ChromeHeaders returns browser-like HTTP headers for direct scraping.
//
// Deprecated: use ChromeHeadersFor(d) with the request's BrowserDoer so the
// User-Agent is derived from the client's identity instead of the default
// profile. Kept for backward compatibility and for callers without a doer in
// hand (tests, ad-hoc scraping).
func ChromeHeaders() map[string]string {
	return ChromeHeadersFor(nil)
}

// IsDDGRateLimited checks whether the DDG response body indicates CAPTCHA.
func IsDDGRateLimited(body []byte) bool {
	low := bytes.ToLower(body)
	for _, marker := range [][]byte{
		[]byte("please try again"),
		[]byte("not a robot"),
		[]byte("unusual traffic"),
		[]byte("blocked"),
	} {
		if bytes.Contains(low, marker) {
			return true
		}
	}
	return bytes.Contains(low, []byte(`action="/d.js"`)) &&
		bytes.Contains(low, []byte(`type="hidden"`))
}

// isStartpageRateLimited checks if Startpage blocked the request.
func isStartpageRateLimited(body []byte) bool {
	lower := bytes.ToLower(body)
	markers := [][]byte{
		[]byte("rate limited"),
		[]byte("too many requests"),
		[]byte("g-recaptcha"),
		[]byte("captcha"),
	}
	for _, m := range markers {
		if bytes.Contains(lower, m) {
			return true
		}
	}
	return false
}

// isRateLimitStatus returns true for HTTP status codes that indicate rate limiting.
func isRateLimitStatus(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusForbidden
}
