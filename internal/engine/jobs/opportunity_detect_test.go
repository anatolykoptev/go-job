package jobs

import "testing"

func TestDetectOpportunityType(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		// Bounty.
		{"github issue", "https://github.com/org/repo/issues/123", "bounty"},
		{"algora", "https://algora.io/bounties/123", "bounty"},
		{"console algora", "https://console.algora.io/org/bounties/123", "bounty"},
		{"opire", "https://opire.dev/bounties/123", "bounty"},
		{"boss", "https://boss.dev/issues/123", "bounty"},
		{"bountyhub", "https://bountyhub.dev/bounties/123", "bounty"},
		// Security.
		{"hackerone", "https://hackerone.com/company", "security"},
		{"bugcrowd", "https://bugcrowd.com/company", "security"},
		{"intigriti", "https://app.intigriti.com/programs/company", "security"},
		{"yeswehack", "https://yeswehack.com/programs/company", "security"},
		{"immunefi", "https://immunefi.com/bounty/company", "security"},
		// Freelance.
		{"remoteok", "https://remoteok.com/remote-jobs/123", "freelance"},
		{"himalayas", "https://himalayas.app/jobs/123", "freelance"},
		{"upwork", "https://upwork.com/freelance-jobs/apply/123", "freelance"},
		{"freelancer", "https://freelancer.com/projects/123", "freelance"},
		{"weworkremotely", "https://weworkremotely.com/remote-jobs/123", "freelance"},
		// Unknown.
		{"unknown", "https://example.com/foo", ""},
		{"empty", "", ""},
		{"invalid", "not-a-url", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectOpportunityType(tt.url)
			if got != tt.want {
				t.Errorf("DetectOpportunityType(%q) = %q, want %q",
					tt.url, got, tt.want)
			}
		})
	}
}
