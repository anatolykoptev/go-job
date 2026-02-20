package jobs

import (
	"sort"
	"strings"
	"unicode"
)

// matchStopWords filters common English words that add noise to keyword matching.
var matchStopWords = map[string]bool{
	"and": true, "the": true, "for": true, "with": true, "you": true,
	"are": true, "have": true, "will": true, "this": true, "that": true,
	"from": true, "our": true, "your": true, "their": true, "they": true,
	"work": true, "team": true, "role": true, "job": true, "join": true,
	"about": true, "which": true, "what": true, "who": true, "how": true,
	"can": true, "not": true, "but": true, "all": true, "also": true,
	"more": true, "than": true, "into": true, "has": true, "its": true,
	"was": true, "were": true, "been": true, "each": true, "new": true,
	"use": true, "using": true, "used": true, "well": true, "high": true,
	"good": true, "able": true, "get": true, "set": true, "such": true,
}

// ExtractResumeKeywords tokenizes resume text into a keyword set (>= 3 chars, lowercased).
// Call once per resume and reuse for batch job scoring.
func ExtractResumeKeywords(text string) map[string]bool {
	return extractMatchKW(text)
}

// extractMatchKW tokenizes text into lowercase keywords, skipping stop words.
// Preserves tech suffixes like "c++", "c#", "node.js" by treating + # . as word chars.
func extractMatchKW(text string) map[string]bool {
	kw := make(map[string]bool)
	var word strings.Builder
	flush := func() {
		w := word.String()
		word.Reset()
		w = strings.TrimRight(w, ".") // drop trailing dots
		if len([]rune(w)) >= 3 && !matchStopWords[w] {
			kw[w] = true
		}
	}
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '+' || r == '#' || r == '.' {
			word.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return kw
}

// ScoreJobMatch computes a Jaccard-based keyword overlap score (0–100)
// between pre-extracted resume keywords and job text.
//
// Returns:
//   - score: 0–100 (Jaccard similarity × 100, rounded to 1 decimal)
//   - matching: keywords present in both resume and job (candidate's strengths)
//   - missing: important job keywords absent from resume (skills gap, top 20 max)
func ScoreJobMatch(resumeKW map[string]bool, jobText string) (score float64, matching, missing []string) {
	jobKW := extractMatchKW(jobText)

	inter := 0
	for kw := range resumeKW {
		if jobKW[kw] {
			inter++
			matching = append(matching, kw)
		}
	}
	for kw := range jobKW {
		if !resumeKW[kw] {
			missing = append(missing, kw)
		}
	}

	union := len(resumeKW) + len(jobKW) - inter
	if union > 0 {
		raw := float64(inter) / float64(union) * 100
		score = float64(int(raw*10+0.5)) / 10 // round to 1 decimal
	}

	sort.Strings(matching)
	sort.Strings(missing)
	if len(missing) > 20 {
		missing = missing[:20]
	}
	return score, matching, missing
}
