package jobs

import (
	"net/url"
	"strings"
)

const (
	oppTypeBounty   = "bounty"
	oppTypeSecurity = "security"
	oppTypeFreelance = "freelance"
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
		return oppTypeBounty
	case strings.Contains(host, "algora.io"):
		return oppTypeBounty
	case strings.Contains(host, "opire.dev"):
		return oppTypeBounty
	case strings.Contains(host, "boss.dev"):
		return oppTypeBounty
	case strings.Contains(host, "bountyhub.dev"):
		return oppTypeBounty
	case strings.Contains(host, "console.algora.io"):
		return oppTypeBounty
	}

	// Security bounty platforms.
	switch {
	case strings.Contains(host, "hackerone.com"):
		return oppTypeSecurity
	case strings.Contains(host, "bugcrowd.com"):
		return oppTypeSecurity
	case strings.Contains(host, "intigriti.com"):
		return oppTypeSecurity
	case strings.Contains(host, "yeswehack.com"):
		return oppTypeSecurity
	case strings.Contains(host, "immunefi.com"):
		return oppTypeSecurity
	}

	// Freelance platforms.
	switch {
	case strings.Contains(host, "remoteok.com"):
		return oppTypeFreelance
	case strings.Contains(host, "himalayas.app"):
		return oppTypeFreelance
	case strings.Contains(host, "upwork.com"):
		return oppTypeFreelance
	case strings.Contains(host, "freelancer.com"):
		return oppTypeFreelance
	case strings.Contains(host, "weworkremotely.com"):
		return oppTypeFreelance
	}

	return ""
}
