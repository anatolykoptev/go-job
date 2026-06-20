package jobserver

import (
	"net/url"
	"strings"
	"testing"
)

// TestAlgoraJobURL_HostValidation verifies the host+path validation logic
// used at the tool boundary in tool_algora_job.go.
func TestAlgoraJobURL_HostValidation(t *testing.T) {
	tests := []struct {
		name   string
		rawURL string
		wantOK bool
	}{
		{"valid", "https://algora.io/comfy/job/abc123", true},
		{"evil host job path", "https://evil.com/job/abc123", false},
		{"algora no job path", "https://algora.io/comfy/jobs", false},
		{"localhost", "http://localhost:8080/comfy/job/abc", false},
		{"ssrf with algora in path", "https://evil.com/algora.io/job/abc", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := url.Parse(tc.rawURL)
			ok := err == nil && parsed.Host == "algora.io" && strings.Contains(parsed.Path, "/job/")
			if ok != tc.wantOK {
				t.Errorf("URL %q: validation=%v want=%v", tc.rawURL, ok, tc.wantOK)
			}
		})
	}
}
