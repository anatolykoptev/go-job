package jobs

import (
	"strings"
	"testing"
)

// TestParseATSURL covers all three platforms, board-level URLs, and unknown URLs.
func TestParseATSURL(t *testing.T) {
	tests := []struct {
		name         string
		rawURL       string
		wantPlatform ATSPlatform
		wantOrg      string
		wantJobID    string
		wantAPIURL   string // substring check
	}{
		// --- Greenhouse job URLs ---
		{
			name:         "greenhouse boards.greenhouse.io job",
			rawURL:       "https://boards.greenhouse.io/stripe/jobs/123456",
			wantPlatform: PlatformGreenhouse,
			wantOrg:      "stripe",
			wantJobID:    "123456",
			wantAPIURL:   "boards-api.greenhouse.io/v1/boards/stripe/jobs",
		},
		{
			name:         "greenhouse job-boards.greenhouse.io job",
			rawURL:       "https://job-boards.greenhouse.io/anthropic/jobs/789",
			wantPlatform: PlatformGreenhouse,
			wantOrg:      "anthropic",
			wantJobID:    "789",
			wantAPIURL:   "boards-api.greenhouse.io/v1/boards/anthropic/jobs",
		},
		{
			name:         "greenhouse board-level URL (no job_id)",
			rawURL:       "https://boards.greenhouse.io/openai",
			wantPlatform: PlatformGreenhouse,
			wantOrg:      "openai",
			wantJobID:    "",
		},
		{
			name:         "greenhouse org casing normalized",
			rawURL:       "https://boards.greenhouse.io/Stripe/jobs/1",
			wantPlatform: PlatformGreenhouse,
			wantOrg:      "stripe",
			wantJobID:    "1",
		},

		// --- Ashby job URLs ---
		{
			name:         "ashby job URL",
			rawURL:       "https://jobs.ashbyhq.com/modal/abc-def-1234-uuid",
			wantPlatform: PlatformAshby,
			wantOrg:      "modal",
			wantJobID:    "abc-def-1234-uuid",
			wantAPIURL:   "api.ashbyhq.com/posting-api/job-board/modal",
		},
		{
			name:         "ashby board-level URL",
			rawURL:       "https://jobs.ashbyhq.com/cursor",
			wantPlatform: PlatformAshby,
			wantOrg:      "cursor",
			wantJobID:    "",
		},
		{
			name:         "ashby org casing normalized",
			rawURL:       "https://jobs.ashbyhq.com/Anysphere/some-uuid",
			wantPlatform: PlatformAshby,
			wantOrg:      "anysphere",
		},

		// --- Lever job URLs ---
		{
			name:         "lever job URL",
			rawURL:       "https://jobs.lever.co/replit/a1b2c3d4-uuid",
			wantPlatform: PlatformLever,
			wantOrg:      "replit",
			wantJobID:    "a1b2c3d4-uuid",
			wantAPIURL:   "api.lever.co/v0/postings/replit",
		},
		{
			name:         "lever board-level URL",
			rawURL:       "https://jobs.lever.co/notion",
			wantPlatform: PlatformLever,
			wantOrg:      "notion",
			wantJobID:    "",
		},
		{
			name:         "lever org casing normalized",
			rawURL:       "https://jobs.lever.co/Perplexity/uuid",
			wantPlatform: PlatformLever,
			wantOrg:      "perplexity",
		},

		// --- Unknown ---
		{
			name:         "linkedin URL → unknown",
			rawURL:       "https://www.linkedin.com/jobs/view/123456",
			wantPlatform: PlatformUnknown,
			wantOrg:      "",
		},
		{
			name:         "empty URL → unknown",
			rawURL:       "",
			wantPlatform: PlatformUnknown,
			wantOrg:      "",
		},
		{
			name:         "generic careers page → unknown",
			rawURL:       "https://careers.openai.com/jobs/researcher",
			wantPlatform: PlatformUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseATSURL(tt.rawURL)
			if err != nil {
				t.Fatalf("ParseATSURL(%q) error: %v", tt.rawURL, err)
			}
			if got.Platform != tt.wantPlatform {
				t.Errorf("platform = %q, want %q", got.Platform, tt.wantPlatform)
			}
			if got.Org != tt.wantOrg {
				t.Errorf("org = %q, want %q", got.Org, tt.wantOrg)
			}
			if tt.wantJobID != "" && got.JobID != tt.wantJobID {
				t.Errorf("job_id = %q, want %q", got.JobID, tt.wantJobID)
			}
			if tt.wantAPIURL != "" && !strings.Contains(got.APIURL, tt.wantAPIURL) {
				t.Errorf("api_url = %q, want to contain %q", got.APIURL, tt.wantAPIURL)
			}
		})
	}
}

// TestParseATSURL_CanonicalURLSet verifies canonical URLs are set for known platforms.
func TestParseATSURL_CanonicalURLSet(t *testing.T) {
	urls := []string{
		"https://boards.greenhouse.io/stripe/jobs/123456",
		"https://jobs.ashbyhq.com/modal/some-uuid",
		"https://jobs.lever.co/replit/some-uuid",
	}
	for _, u := range urls {
		got, err := ParseATSURL(u)
		if err != nil {
			t.Fatalf("ParseATSURL(%q) error: %v", u, err)
		}
		if got.CanonicalURL == "" {
			t.Errorf("ParseATSURL(%q) CanonicalURL is empty", u)
		}
	}
}
