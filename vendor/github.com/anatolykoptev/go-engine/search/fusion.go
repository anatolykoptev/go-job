package search

import (
	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-engine/websearch"
)

// FuseWRR merges multiple result sets using Weighted Reciprocal Rank.
func FuseWRR(resultSets [][]sources.Result, weights []float64) []sources.Result {
	return websearch.FuseWRR(resultSets, weights)
}
