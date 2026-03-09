package jobs

import (
	"net/url"
	"strings"
)

// DetectOpportunityType determines the opportunity type from a URL.
// Returns "bounty", "security", "freelance", or "" if unknown.
func DetectOpportunityType(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}

	host := strings.ToLower(u.Hostname())
	path := strings.ToLower(u.Path)

	// Bounty platforms.
	switch {
	case host == "github.com" && strings.Contains(path, "/issues/"):
		return "bounty"
	case strings.Contains(host, "algora.io"):
		return "bounty"
	case strings.Contains(host, "opire.dev"):
		return "bounty"
	case strings.Contains(host, "boss.dev"):
		return "bounty"
	case strings.Contains(host, "bountyhub.dev"):
		return "bounty"
	case strings.Contains(host, "console.algora.io"):
		return "bounty"
	}

	// Security bounty platforms.
	switch {
	case strings.Contains(host, "hackerone.com"):
		return "security"
	case strings.Contains(host, "bugcrowd.com"):
		return "security"
	case strings.Contains(host, "intigriti.com"):
		return "security"
	case strings.Contains(host, "yeswehack.com"):
		return "security"
	case strings.Contains(host, "immunefi.com"):
		return "security"
	}

	// Freelance platforms.
	switch {
	case strings.Contains(host, "remoteok.com"):
		return "freelance"
	case strings.Contains(host, "himalayas.app"):
		return "freelance"
	case strings.Contains(host, "upwork.com"):
		return "freelance"
	case strings.Contains(host, "freelancer.com"):
		return "freelance"
	case strings.Contains(host, "weworkremotely.com"):
		return "freelance"
	}

	return ""
}
