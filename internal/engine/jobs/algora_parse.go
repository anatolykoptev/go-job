package jobs

import (
	"strings"
)

// extractOrgFromGitHubURL extracts "org/repo" from a GitHub URL.
func extractOrgFromGitHubURL(ghURL string) string {
	// https://github.com/org/repo/issues/123 → "org/repo"
	const prefix = "https://github.com/"
	if !strings.HasPrefix(ghURL, prefix) {
		return ""
	}
	path := strings.TrimPrefix(ghURL, prefix)
	parts := strings.Split(path, "/")
	if len(parts) >= 2 {
		return parts[0] + "/" + parts[1]
	}
	return path
}

// extractTitleFromBlock extracts a human-readable title from the HTML block
// between the bounty amount and the github link. Strips tags and picks the
// best text segment.
func extractTitleFromBlock(block, amount string) string {
	// Find the position of the amount in the block.
	amtIdx := strings.LastIndex(block, amount)
	if amtIdx < 0 {
		return ""
	}

	// Take text after the amount to end of block.
	after := block[amtIdx+len(amount):]

	// Strip HTML tags.
	plain := reHTMLTag.ReplaceAllString(after, " ")
	// Normalize whitespace.
	plain = reWhitespace.ReplaceAllString(strings.TrimSpace(plain), " ")

	// Remove common link text artifacts that appear at the end.
	for _, noise := range []string{" View", " view", " View Issue"} {
		plain = strings.TrimSuffix(plain, noise)
	}
	plain = strings.TrimSpace(plain)

	// The title is usually the first meaningful segment.
	// Split by common delimiters and take the longest non-trivial segment.
	segments := strings.FieldsFunc(plain, func(r rune) bool {
		return r == '|' || r == '·'
	})

	var best string
	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		// Skip short noise.
		if len(seg) < 5 {
			continue
		}
		// Take the first substantial segment — it's typically the title.
		if best == "" || (len(seg) > len(best) && len(best) < 20) {
			best = seg
		}
		if len(best) >= 20 {
			break
		}
	}

	if best == "" {
		return ""
	}

	// Truncate if too long (HTML artifacts).
	if len([]rune(best)) > 120 {
		best = string([]rune(best)[:120])
	}
	return best
}

// isTitleNoise returns true if the title is a scraping artifact (e.g. "tip 14 hours ago").
func isTitleNoise(title string) bool {
	if reTitleNoise.MatchString(strings.TrimSpace(title)) {
		return true
	}
	// Reject titles starting with "tip" (algora tip timestamps).
	lower := strings.ToLower(strings.TrimSpace(title))
	if strings.HasPrefix(lower, "tip ") && strings.Contains(lower, " ago") {
		return true
	}
	return false
}
