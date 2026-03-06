package search

import (
	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-stealth/websearch"
)

// FuseWRR merges multiple result sets using Weighted Reciprocal Rank.
// Each result's fused score = sum of weight[i] / (k + rank) across sources.
// Results are grouped by URL — duplicates accumulate score.
// Returns results sorted by fused score descending.
// Delegates to websearch.FuseWRR.
func FuseWRR(resultSets [][]sources.Result, weights []float64) []sources.Result {
	wsSets := make([][]websearch.Result, len(resultSets))
	for i, set := range resultSets {
		wsSets[i] = sourceToWSResults(set)
	}
	return wsToSourceResults(websearch.FuseWRR(wsSets, weights))
}
