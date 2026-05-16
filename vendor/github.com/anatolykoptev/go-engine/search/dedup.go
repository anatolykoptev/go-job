package search

import (
	"math"
	"strings"
	"unicode"

	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-engine/websearch"
)

// DedupSnippets removes near-duplicate results based on BoW cosine similarity.
// Delegates to websearch.DedupSnippets.
func DedupSnippets(results []sources.Result, threshold float64) []sources.Result {
	return websearch.DedupSnippets(results, threshold)
}

// tokenize converts text to a bag-of-words frequency vector.
// Kept for test compatibility; logic matches websearch internal tokenize.
func tokenize(s string) map[string]float64 {
	vec := make(map[string]float64)
	words := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for _, w := range words {
		vec[w]++
	}
	return vec
}

// cosineSimilarity computes cosine similarity between two BoW vectors.
// Kept for test compatibility; logic matches websearch internal cosineSimilarity.
func cosineSimilarity(a, b map[string]float64) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for k, v := range a {
		normA += v * v
		if bv, ok := b[k]; ok {
			dot += v * bv
		}
	}
	for _, v := range b {
		normB += v * v
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}
