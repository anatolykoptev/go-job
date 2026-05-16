package websearch

import "net/url"

// FilterByScore removes results below minScore, keeping at least minKeep.
func FilterByScore(results []Result, minScore float64, minKeep int) []Result {
	var out []Result
	for _, r := range results {
		if r.Score >= minScore {
			out = append(out, r)
		}
	}
	if len(out) < minKeep && len(results) >= minKeep {
		return results[:minKeep]
	}
	if len(out) < minKeep {
		return results
	}
	return out
}

// DedupByDomain limits results to maxPerDomain per domain.
// High-score results (score >= highScoreThreshold) bypass the per-domain limit.
// Pass highScoreThreshold=0 to disable bypass (strict mode, old behavior).
func DedupByDomain(results []Result, maxPerDomain int, highScoreThreshold ...float64) []Result {
	threshold := 0.0
	if len(highScoreThreshold) > 0 {
		threshold = highScoreThreshold[0]
	}

	counts := make(map[string]int)
	var out []Result
	for _, r := range results {
		u, err := url.Parse(r.URL)
		if err != nil {
			continue
		}
		domain := u.Hostname()
		if threshold > 0 && r.Score >= threshold {
			// High-score results always pass through.
			out = append(out, r)
			counts[domain]++
			continue
		}
		if counts[domain] < maxPerDomain {
			out = append(out, r)
			counts[domain]++
		}
	}
	return out
}
