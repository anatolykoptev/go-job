package websearch

import (
	"bytes"
	"net/http"
	"regexp"
	"strings"

	stealth "github.com/anatolykoptev/go-stealth"
)

var reHTMLTag = regexp.MustCompile(`<[^>]*>`)

// CleanHTML strips HTML tags and trims whitespace.
func CleanHTML(s string) string {
	return strings.TrimSpace(reHTMLTag.ReplaceAllString(s, ""))
}

// ChromeHeaders returns browser-like HTTP headers for direct scraping.
func ChromeHeaders() map[string]string {
	return stealth.ChromeHeaders()
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
