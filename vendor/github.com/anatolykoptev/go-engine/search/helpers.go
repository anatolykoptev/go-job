package search

import "bytes"

// isDDGRateLimited checks whether the DDG response body indicates a CAPTCHA
// or rate-limit page. Mirrors websearch.isDDGRateLimited (unexported).
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
	// DDG captcha form: action="/d.js" combined with type="hidden".
	return bytes.Contains(low, []byte(`action="/d.js"`)) &&
		bytes.Contains(low, []byte(`type="hidden"`))
}

// startpageCheckRateLimit returns true when the response body contains
// markers indicating Startpage has blocked or rate-limited the request.
// Mirrors websearch.isStartpageRateLimited (unexported).
func startpageCheckRateLimit(body []byte) bool {
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
