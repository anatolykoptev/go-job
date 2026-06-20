package jobserver

import (
	"testing"
)

func TestValidateAlgoraJobURL(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"valid algora url", "https://algora.io/comfy/job/abc123", false},
		{"wrong host", "https://evil.com/jobs/123", true},
		{"algora with port", "https://algora.io:443/jobs/123", false},
		{"not algora subdomain", "https://sub.algora.io/jobs/123", true},
		{"empty url", "", true},
		{"ssrf algora in path", "https://evil.com/algora.io/job/abc", true},
		{"localhost", "http://localhost:8080/comfy/job/abc", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateAlgoraJobURL(tc.url)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateAlgoraJobURL(%q) err=%v, wantErr=%v", tc.url, err, tc.wantErr)
			}
		})
	}
}
