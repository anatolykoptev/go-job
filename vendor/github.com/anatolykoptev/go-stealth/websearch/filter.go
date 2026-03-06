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
func DedupByDomain(results []Result, maxPerDomain int) []Result {
	counts := make(map[string]int)
	var out []Result
	for _, r := range results {
		u, err := url.Parse(r.URL)
		if err != nil {
			continue
		}
		domain := u.Hostname()
		if counts[domain] < maxPerDomain {
			out = append(out, r)
			counts[domain]++
		}
	}
	return out
}
