package search

import (
	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-stealth/websearch"
)

// FilterByScore removes results below minScore, keeping at least minKeep.
// Delegates to websearch.FilterByScore.
func FilterByScore(results []sources.Result, minScore float64, minKeep int) []sources.Result {
	ws := sourceToWSResults(results)
	filtered := websearch.FilterByScore(ws, minScore, minKeep)
	return wsToSourceResults(filtered)
}

// DedupByDomain limits results to maxPerDomain per domain.
// Delegates to websearch.DedupByDomain.
func DedupByDomain(results []sources.Result, maxPerDomain int) []sources.Result {
	ws := sourceToWSResults(results)
	deduped := websearch.DedupByDomain(ws, maxPerDomain)
	return wsToSourceResults(deduped)
}
