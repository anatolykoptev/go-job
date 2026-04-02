package linkedin

import (
	"regexp"
	"strings"
)

var csrfRe = regexp.MustCompile(`<input[^>]+name="loginCsrfParam"[^>]+value="([^"]*)"`)

// parseCSRFToken extracts loginCsrfParam value from HTML.
func parseCSRFToken(body []byte) string {
	matches := csrfRe.FindSubmatch(body)
	if len(matches) < 2 {
		return ""
	}
	return string(matches[1])
}

// cookieAttrs are Set-Cookie attributes that should not be treated as cookie names.
var cookieAttrs = map[string]bool{
	"path": true, "domain": true, "expires": true, "max-age": true,
	"secure": true, "httponly": true, "samesite": true, "partitioned": true,
}

// parseJoinedSetCookies parses the "; "-joined Set-Cookie string from go-stealth.
// go-stealth joins multiple Set-Cookie headers with "; ", producing e.g.:
// "li_at=AQED123; Path=/; Secure; JSESSIONID=ajax:456; Path=/; Secure"
func parseJoinedSetCookies(raw string) map[string]string {
	cookies := make(map[string]string)
	if raw == "" {
		return cookies
	}
	parts := strings.Split(raw, "; ")
	for _, part := range parts {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue // bare attribute like "Secure" or "HttpOnly"
		}
		k = strings.TrimSpace(k)
		if cookieAttrs[strings.ToLower(k)] {
			continue // skip cookie attributes
		}
		cookies[k] = strings.TrimSpace(v)
	}
	return cookies
}

func resolveURL(location string) string {
	if strings.HasPrefix(location, "/") {
		return baseURL + location
	}
	return location
}
