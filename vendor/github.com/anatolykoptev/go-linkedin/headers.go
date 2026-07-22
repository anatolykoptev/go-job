package linkedin

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	defaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	defaultSecChUA   = `"Chromium";v="131", "Not-A.Brand";v="24", "Google Chrome";v="131"`
	voyagerAccept    = "application/vnd.linkedin.normalized+json+2.1"
	restliVersion    = "2.0.0"
)

func buildHeaders(csrfToken, clientVersion, userAgent, secChUA string) (map[string]string, error) {
	if userAgent == "" {
		userAgent = defaultUserAgent
	}
	if secChUA == "" {
		secChUA = defaultSecChUA
	}
	liTrack, err := buildLiTrack(clientVersion)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"Accept":                    voyagerAccept,
		"Accept-Language":           "en-US,en;q=0.9",
		"csrf-token":                csrfToken,
		"x-li-track":                liTrack,
		"x-restli-protocol-version": restliVersion,
		"User-Agent":                userAgent,
		"sec-ch-ua":                 secChUA,
		"sec-ch-ua-mobile":          "?0",
		"sec-ch-ua-platform":        `"macOS"`,
		"sec-fetch-dest":            "empty",
		"sec-fetch-mode":            "cors",
		"sec-fetch-site":            "same-origin",
	}, nil
}

func buildLiTrack(clientVersion string) (string, error) {
	if clientVersion == "" {
		return "", fmt.Errorf("buildLiTrack: clientVersion is empty")
	}
	track := map[string]string{
		"clientVersion":    clientVersion,
		"mpVersion":        clientVersion,
		"osName":           "web",
		"timezoneOffset":   "0",
		"deviceFormFactor": "DESKTOP",
		"mpName":           "voyager-web",
		"displayDensity":   "2",
		"displayWidth":     "1920",
		"displayHeight":    "1080",
	}
	b, err := json.Marshal(track)
	if err != nil {
		return "", fmt.Errorf("buildLiTrack: marshal: %w", err)
	}
	return string(b), nil
}

func buildCookieString(cookies map[string]string) string {
	parts := make([]string, 0, len(cookies))
	for k, v := range cookies {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return strings.Join(parts, "; ")
}

func extractCSRFToken(jsessionID string) string {
	return strings.Trim(jsessionID, `"`)
}
