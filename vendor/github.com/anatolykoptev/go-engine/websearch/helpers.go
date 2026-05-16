package websearch

import (
	"bytes"
	"math/rand/v2"
	"net/http"
	"regexp"
	"strings"
)

var reHTMLTag = regexp.MustCompile(`<[^>]*>`)

// Common Accept header values reused across search providers.
const (
	acceptJSON = "application/json"
	acceptHTML = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
)

// CleanHTML strips HTML tags and trims whitespace.
func CleanHTML(s string) string {
	return strings.TrimSpace(reHTMLTag.ReplaceAllString(s, ""))
}

// chromeUserAgents is a pool of Chrome-like User-Agents for rotation.
var chromeUserAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.2 Safari/605.1.15",
	"Mozilla/5.0 (X11; Linux x86_64; rv:109.0) Gecko/20100101 Firefox/115.0",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:109.0) Gecko/20100101 Firefox/115.0",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:109.0) Gecko/20100101 Firefox/115.0",
}

// ChromeHeaders returns browser-like HTTP headers for direct scraping.
func ChromeHeaders() map[string]string {
	return map[string]string{
		"accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
		"accept-language": "en-US,en;q=0.9",
		"accept-encoding": "gzip, deflate, br",
		"user-agent":      chromeUserAgents[rand.IntN(len(chromeUserAgents))], //nolint:gosec // not crypto
	}
}

// isDDGRateLimited checks whether the DDG response body indicates CAPTCHA.
func isDDGRateLimited(body []byte) bool {
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
